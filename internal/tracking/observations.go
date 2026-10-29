package tracking

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strconv"
)

type observation struct {
	repo                   *repository
	previous               *ProjectRow
	typ                    string
	listedAt               *int64
	observedAt, metadataAt int64
}

func rowRepository(row ProjectRow) (*repository, error) {
	var names []string
	if e := json.Unmarshal([]byte(row.Topics), &names); e != nil {
		return nil, e
	}
	r := &repository{ID: row.RepoID, Name: row.Name, Description: row.Description, Stars: ptr(row.Stars)}
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
func jsonText(v any) string { b, _ := json.Marshal(v); return string(b) }
func (s *Store) saveObservations(ctx context.Context, items []observation, now int64, policy *SamplingPolicy) error {
	return s.saveObservationsAnd(ctx, items, now, policy, nil)
}

// The hook commits an intake checkpoint with the observations it created.
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
		name, description, language, topics := o.repo.Name, o.repo.Description, (*string)(nil), "[]"
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
			language = o.previous.Language
			topics = o.previous.Topics
			if metadataAt == 0 {
				metadataAt = o.previous.MetadataAt
			}
		}
		listed := now
		if o.listedAt != nil {
			listed = *o.listedAt
		} else if o.previous != nil {
			listed = o.previous.ListedAt
		}
		if o.previous != nil {
			_, e = tx.ExecContext(ctx, `UPDATE projects SET name=?,description=?,language=?,topics=?,observed_at=?,attempted_at=?,stars=?,trends=?,error=NULL,metadata_at=?,listed_at=MAX(listed_at,?) WHERE id=? AND cycle=? AND observed_at<?`, name, description, language, topics, at, at, *o.repo.Stars, jsonText(trends), metadataAt, listed, id, cycle, at)
		} else {
			_, e = tx.ExecContext(ctx, `INSERT INTO projects(id,type,repo_id,cycle,name,description,language,topics,observed_at,attempted_at,stars,trends,entered_at,listed_at,metadata_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, id, typ, o.repo.ID, cycle, name, description, language, topics, at, at, *o.repo.Stars, jsonText(trends), now, listed, metadataAt)
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
	if checkpoint != nil {
		if e = checkpoint(tx); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func setTxState(ctx context.Context, tx *sql.Tx, key string, value any) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO system_state(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, jsonText(value))
	return e
}
