package tracking

// 本文件按追踪类型分别执行每日淘汰。
// 结合 trends.go 理解保留评分，结合 observations.go 理解追踪周期与历史归属。

import (
	"context"
	"slices"
)

// cleanupResult 记录某个追踪类型本日淘汰的执行时段、删除数量、剩余数量与符合淘汰条件的数量。
type cleanupResult struct {
	At        int64 `json:"at"`
	Removed   int   `json:"removed"`
	Remaining int   `json:"remaining"`
	Eligible  int   `json:"eligible"`
}

// cleanup 按追踪类型独立执行每日淘汰，500 项是软目标，保护期或观测不足时允许超出。
// 仅考虑入库满 24 小时、观测新鲜且历史可比较的项目；综合窗口增长速度得到保留评分。
// 按评分、24 小时净增量、Star 总数、入库时间、ID 升序淘汰，删除和执行记录同事务提交。
func (s *Scheduler) cleanup(ctx context.Context, now int64, policy *SamplingPolicy) (bool, error) {
	// 每日淘汰边界是北京时间 04:00（UTC 前一天 20:00），不是程序每启动就重复淘汰。
	at := floorDiv(now+4*Hour, Day)*Day - 4*Hour
	changed := false
	for _, typ := range projectTypes {
		var previous cleanupResult
		found, e := s.store.State("cleanup:"+typ, &previous)
		if e != nil {
			return false, e
		}
		if !found && typ == "github" {
			found, e = s.store.State("cleanup", &previous)
			if e != nil {
				return false, e
			}
		}
		if found && previous.At >= at {
			continue
		}
		rows, e := s.store.ProjectRows("WHERE type=?", typ)
		if e != nil {
			return false, e
		}
		potential := []ProjectRow{}
		for _, row := range rows {
			if now-row.EnteredAt >= Day && !IsStale(row.ObservedAt, now, policy) {
				potential = append(potential, row)
			}
		}
		type candidate struct {
			row   ProjectRow
			score float64
			delta int64
		}
		candidates := []candidate{}
		if len(rows) > 500 {
			views, e := s.store.ProjectViews(potential, now, policy)
			if e != nil {
				return false, e
			}
			for i, view := range views {
				longest := view.Trends[720]
				if longest.Start == nil || longest.End == nil || *longest.End-*longest.Start < Day {
					continue
				}
				score := float64(0)
				valid := true
				for j, width := range Windows {
					v := view.Trends[width]
					if v.Delta == nil || v.Start == nil || v.End == nil || *v.End <= *v.Start {
						valid = false
						break
					}
					// 先把净增量下限夹到 0，再除以实际观测天数得到速度，按四个窗口权重相加；
					// 负净增量不产生负评分，但稍后的 24 小时净增量决胜规则仍保留其负值。
					score += float64(max(*v.Delta, 0)) / (float64(*v.End-*v.Start) / float64(Day)) * []float64{.6, .25, .1, .05}[j]
				}
				if valid {
					candidates = append(candidates, candidate{potential[i], score, *view.Trends[24].Delta})
				}
			}
		}
		// 比较函数约定：负数代表 a 排在前，0 表示同序，正数代表 b 排在前，类似 Java Comparator。
		slices.SortFunc(candidates, func(a, b candidate) int {
			if a.score < b.score {
				return -1
			}
			if a.score > b.score {
				return 1
			}
			if a.delta < b.delta {
				return -1
			}
			if a.delta > b.delta {
				return 1
			}
			if a.row.Stars < b.row.Stars {
				return -1
			}
			if a.row.Stars > b.row.Stars {
				return 1
			}
			if a.row.EnteredAt < b.row.EnteredAt {
				return -1
			}
			if a.row.EnteredAt > b.row.EnteredAt {
				return 1
			}
			return EnglishCompare(a.row.ID, b.row.ID)
		})
		// 最多删到 500 个；符合条件不足时不强删受保护/数据不足项目，所以是软容量。
		victims := candidates[:min(len(candidates), max(0, len(rows)-500))]
		tx, e := s.store.DB.BeginTx(ctx, nil)
		if e != nil {
			return false, e
		}
		for _, victim := range victims {
			if _, e = tx.ExecContext(ctx, "DELETE FROM projects WHERE id=? AND cycle=? AND entered_at<=?", victim.row.ID, victim.row.Cycle, now-Day); e != nil {
				tx.Rollback()
				return false, e
			}
		}
		if e = setTxState(ctx, tx, "cleanup:"+typ, cleanupResult{At: at, Removed: len(victims), Remaining: len(rows) - len(victims), Eligible: len(candidates)}); e != nil {
			tx.Rollback()
			return false, e
		}
		if e = tx.Commit(); e != nil {
			return false, e
		}
		changed = true
	}
	return changed, nil
}
