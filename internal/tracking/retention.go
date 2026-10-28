package tracking

import (
	"context"
	"slices"
)

type cleanupResult struct {
	At        int64 `json:"at"`
	Removed   int   `json:"removed"`
	Remaining int   `json:"remaining"`
	Eligible  int   `json:"eligible"`
}

func (s *Scheduler) cleanup(ctx context.Context, now int64, policy *SamplingPolicy) (bool, error) {
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
					score += float64(max(*v.Delta, 0)) / (float64(*v.End-*v.Start) / float64(Day)) * []float64{.6, .25, .1, .05}[j]
				}
				if valid {
					candidates = append(candidates, candidate{potential[i], score, *view.Trends[24].Delta})
				}
			}
		}
		slices.SortFunc(candidates, func(a, b candidate) int {
			if a.score < b.score {
				return -1
			}
			if a.score > b.score {
				return 1
			}
			if a.row.ListedAt < b.row.ListedAt {
				return -1
			}
			if a.row.ListedAt > b.row.ListedAt {
				return 1
			}
			if a.delta < b.delta {
				return -1
			}
			if a.delta > b.delta {
				return 1
			}
			return EnglishCompare(a.row.ID, b.row.ID)
		})
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
