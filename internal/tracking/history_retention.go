package tracking

import (
	"context"
	"encoding/json"
	"log"
)

// pruneHistory checks each retained bucket against the union before deleting anything.
// Reads finish before the transaction because SQLite uses a single connection.
func (s *Store) pruneHistory(ctx context.Context, now int64, policy *SamplingPolicy) (bool, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT d.cycle,d.day,d.hours,p.observed_at FROM snapshot_days d JOIN projects p ON p.cycle=d.cycle`)
	if err != nil {
		return false, err
	}
	type bucket struct {
		cycle string
		day   int64
		hours map[string]Point
	}
	changes := []bucket{}
	removed := 0
	for rows.Next() {
		var b bucket
		var raw string
		var end int64
		if err = rows.Scan(&b.cycle, &b.day, &raw, &end); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &b.hours); err != nil {
			break
		}
		count := len(b.hours)
		for key, point := range b.hours {
			keep := point[0] == end
			if point[0] >= now-Retention && point[0] <= end {
				for _, width := range Windows {
					if point[0] >= end-int64(width)*Hour && windowSlot(SamplingSlot(point[0], policy), width) {
						keep = true
						break
					}
				}
			}
			if !keep {
				delete(b.hours, key)
			}
		}
		if len(b.hours) < count {
			removed += count - len(b.hours)
			changes = append(changes, b)
		}
	}
	scanErr := rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	if scanErr != nil {
		return false, scanErr
	}
	if len(changes) == 0 {
		return false, nil
	}
	log.Printf("history retention checked %d changed buckets; pruning %d redundant snapshots", len(changes), removed)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	for _, b := range changes {
		if len(b.hours) == 0 {
			_, err = tx.ExecContext(ctx, "DELETE FROM snapshot_days WHERE cycle=? AND day=?", b.cycle, b.day)
		} else {
			_, err = tx.ExecContext(ctx, "UPDATE snapshot_days SET hours=? WHERE cycle=? AND day=?", jsonText(b.hours), b.cycle, b.day)
		}
		if err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}
