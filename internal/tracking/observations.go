package tracking

// 本文件把共享的仓库观测写入各自独立的追踪项目与快照历史。
// 核心入口 saveObservationsAnd 将观测与调用方提供的检查点放进同一个事务。

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strconv"
)

// observation 描述一次待写入观测：repo 可被两种追踪类型共用，previous 决定更新哪个原有周期。
// previous 为 nil 表示新收录；真实观测时间与本次收录时间可以不同。
type observation struct {
	repo                   *repository
	previous               *ProjectRow
	typ                    string
	observedAt, metadataAt int64
}

// rowRepository 把数据库行还原成上游仓库结构，以复用同轮已成功观测，避免重复请求。
// 只复制本次资料和 Star，不复制另一追踪类型的历史。
func rowRepository(row ProjectRow) (*repository, error) {
	var names []string
	if e := json.Unmarshal([]byte(row.Topics), &names); e != nil {
		return nil, e
	}
	r := &repository{ID: row.RepoID, Name: row.Name, Description: row.Description, Homepage: row.Homepage, Stars: ptr(row.Stars)}
	if row.Language != nil {
		r.Language = &struct {
			Name string `json:"name"`
		}{*row.Language}
	}
	r.Topics = &struct {
		Nodes []struct {
			Topic struct {
				Name string `json:"name"`
			} `json:"topic"`
		} `json:"nodes"`
	}{}
	for _, name := range names {
		node := struct {
			Topic struct {
				Name string `json:"name"`
			} `json:"topic"`
		}{}
		node.Topic.Name = name
		r.Topics.Nodes = append(r.Topics.Nodes, node)
	}
	return r, nil
}

// jsonText 将本包已知可编码的数据转为 JSON 文本；这里忽略错误，所以不适用于任意未经约束的输入。
func jsonText(v any) string { b, _ := json.Marshal(v); return string(b) }

// saveObservations 保存一批观测，不附加调用方检查点；具体事务逻辑统一委托给 saveObservationsAnd。
func (s *Store) saveObservations(ctx context.Context, items []observation, now int64, policy *SamplingPolicy) error {
	return s.saveObservationsAnd(ctx, items, now, policy, nil)
}

// saveObservationsAnd 原子保存项目、小时快照、趋势，以及可选的收录检查点；任一步失败则回滚。
// checkpoint 是函数参数，类似传入 Java 回调，但不必先定义单独接口；nil 表示无需额外动作。
// 新项目随机生成追踪周期，旧项目用 ID+周期+时间条件阻止迟到观测写入替代周期。
func (s *Store) saveObservationsAnd(ctx context.Context, items []observation, now int64, policy *SamplingPolicy, checkpoint func(*sql.Tx) error) error {
	if len(items) == 0 && checkpoint == nil {
		return nil
	}
	cycles := []string{}
	for _, o := range items {
		if o.previous != nil {
			cycles = append(cycles, o.previous.Cycle)
		}
	}
	// 先读历史再开启写事务；单连接池中不能持有 Tx 又通过 s.DB 发起另一个查询。
	histories, e := s.Histories(cycles, now)
	if e != nil {
		return e
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, o := range items {
		at := o.observedAt
		if at == 0 {
			at = now
		}
		typ := o.typ
		if typ == "" && o.previous != nil {
			typ = o.previous.Type
		}
		if typ == "" {
			typ = "github"
		}
		id := typ + ":" + o.repo.ID
		cycle := ""
		if o.previous != nil {
			cycle = o.previous.Cycle
		} else {
			// 固定 16 字节数组提供周期随机身份；token[:] 把整个数组变成 slice 传给 rand.Read。
			var token [16]byte
			if _, e = rand.Read(token[:]); e != nil {
				return e
			}
			cycle = hex.EncodeToString(token[:])
		}
		gaps := []Gap{}
		if previous := o.previous; previous != nil {
			var stored map[int]TrendSummary
			if e = json.Unmarshal([]byte(previous.Trends), &stored); e != nil {
				return e
			}
			gaps = stored[720].Gaps
			if gaps == nil {
				gaps = []Gap{}
			}
			if previous.ObservedAt < at {
				if floorDiv(previous.ObservedAt, Hour) == floorDiv(at, Hour) {
					if len(gaps) > 0 && gaps[len(gaps)-1][1] == previous.ObservedAt {
						gaps[len(gaps)-1][1] = at
					}
				} else if MissedSampling(previous.ObservedAt, at, policy) {
					gaps = append(gaps, Gap{previous.ObservedAt, at})
				}
			}
		}
		trends := map[int]TrendSummary{}
		for _, width := range Windows {
			first := Point{at, *o.repo.Stars}
			for _, p := range histories[cycle] {
				if p[0] >= max(now-Retention, at-int64(width)*Hour) && p[0] <= at && floorDiv(p[0], Hour) != floorDiv(at, Hour) {
					first = p
					break
				}
			}
			v := TrendSummary{Start: ptr(first[0]), End: ptr(at), State: "accumulating", Gaps: []Gap{}}
			if at > first[0] {
				v.Delta = ptr(*o.repo.Stars - first[1])
				v.State = "partial"
				if CompleteWindow(first[0], at, width, policy) {
					v.State = "complete"
				}
			}
			for _, gap := range gaps {
				if gap[0] >= first[0] && gap[1] <= at {
					v.Gaps = append(v.Gaps, gap)
				}
			}
			trends[width] = v
		}
		// (*string)(nil) 是带具体类型的 nil，便于多重短声明推断 language 为 *string。
		// Go 不能单独用 x := nil 推断类型；必须通过上下文或显式类型确定。
		name, description, homepage, language, topics := o.repo.Name, o.repo.Description, o.repo.Homepage, (*string)(nil), "[]"
		metadataAt := o.metadataAt
		if o.repo.Name != "" {
			names := []string{}
			if o.repo.Topics != nil {
				for _, n := range o.repo.Topics.Nodes {
					names = append(names, n.Topic.Name)
				}
			}
			topics = jsonText(names)
			if o.repo.Language != nil {
				language = strptr(o.repo.Language.Name)
			}
			if metadataAt == 0 {
				metadataAt = at
			}
		} else if o.previous != nil {
			name = o.previous.Name
			description = o.previous.Description
			homepage = o.previous.Homepage
			language = o.previous.Language
			topics = o.previous.Topics
			if metadataAt == 0 {
				metadataAt = o.previous.MetadataAt
			}
		}
		// 更新附带原周期与 observed_at 条件；迟到结果不能回退时间，也不能写进同 ID 的新周期。
		// 下面快照 INSERT ... SELECT 也验证当前周期/时间，避免项目没更新却留下孤立旧结果。
		if o.previous != nil {
			_, e = tx.ExecContext(ctx, `UPDATE projects SET name=?,description=?,homepage=?,language=?,topics=?,observed_at=?,attempted_at=?,stars=?,trends=?,error=NULL,metadata_at=? WHERE id=? AND cycle=? AND observed_at<?`, name, description, homepage, language, topics, at, at, *o.repo.Stars, jsonText(trends), metadataAt, id, cycle, at)
		} else {
			_, e = tx.ExecContext(ctx, `INSERT INTO projects(id,type,repo_id,cycle,name,description,homepage,language,topics,observed_at,attempted_at,stars,trends,entered_at,metadata_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, id, typ, o.repo.ID, cycle, name, description, homepage, language, topics, at, at, *o.repo.Stars, jsonText(trends), now, metadataAt)
		}
		if e != nil {
			return e
		}
		hour := strconv.FormatInt(floorDiv(at, Hour), 10)
		path := `$."` + hour + `"`
		point := jsonText(Point{at, *o.repo.Stars})
		_, e = tx.ExecContext(ctx, `INSERT INTO snapshot_days(cycle,day,hours) SELECT ?,?,json_object(?,json(?)) FROM projects WHERE id=? AND cycle=? AND observed_at=? ON CONFLICT(cycle,day) DO UPDATE SET hours=json_set(snapshot_days.hours,?,json(?)) WHERE COALESCE(json_extract(snapshot_days.hours,? || '[0]'),0) < ?`, cycle, floorDiv(at, Day), hour, point, id, cycle, at, path, point, path, at)
		if e != nil {
			return e
		}
	}
	// 仍在同一个 Tx 上调用检查点；例如投稿阶段推进失败，前面的项目/快照写入也全部回滚。
	if checkpoint != nil {
		if e = checkpoint(tx); e != nil {
			return e
		}
	}
	return tx.Commit()
}

// setTxState 在调用方已开启的事务中保存 JSON 状态，避免另开连接破坏原子性或阻塞单连接池。
func setTxState(ctx context.Context, tx *sql.Tx, key string, value any) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO system_state(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, jsonText(value))
	return e
}
