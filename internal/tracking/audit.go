package tracking

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

type TableDigest struct {
	Rows   int    `json:"rows"`
	SHA256 string `json:"sha256"`
}
type ProjectCheckpoint struct {
	ID        string `json:"id"`
	Cycle     string `json:"cycle"`
	EnteredAt int64  `json:"enteredAt"`
}
type DatabaseAudit struct {
	Tables           map[string]TableDigest `json:"tables"`
	ProjectTypes     map[string]int         `json:"projectTypes"`
	Repositories     int                    `json:"repositories"`
	Cycles           int                    `json:"cycles"`
	SnapshotPoints   int                    `json:"snapshotPoints"`
	Earliest         *int64                 `json:"earliest"`
	Latest           *int64                 `json:"latest"`
	Projects         []ProjectCheckpoint    `json:"projects"`
	StateCheckpoints map[string]any         `json:"stateCheckpoints"`
	Integrity        string                 `json:"integrity"`
	ForeignKeys      string                 `json:"foreignKeys"`
}

// Audit checks the entire physical dataset, including history older than the
// query horizon. JSON text is hashed by parsed content rather than key order.
func Audit(db *sql.DB) (DatabaseAudit, error) {
	report := DatabaseAudit{Tables: map[string]TableDigest{}, ProjectTypes: map[string]int{}, Projects: []ProjectCheckpoint{}, StateCheckpoints: map[string]any{}}
	if err := CheckDatabase(db); err != nil {
		return report, err
	}
	report.Integrity = "ok"
	report.ForeignKeys = "ok"
	tables := []string{"projects", "snapshot_days", "system_state"}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return report, err
	}
	if version >= 4 {
		tables = append(tables, "submissions")
	}
	for _, table := range tables {
		query := "SELECT " + strings.Join(tableColumns[table], ",") + " FROM " + table
		switch table {
		case "projects", "submissions":
			query += " ORDER BY id"
		case "snapshot_days":
			query += " ORDER BY cycle,day"
		case "system_state":
			query += " ORDER BY key"
		}
		rows, err := db.Query(query)
		if err != nil {
			return report, err
		}
		hash := sha256.New()
		count := 0
		for rows.Next() {
			values := make([]any, len(tableColumns[table]))
			pointers := make([]any, len(values))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				rows.Close()
				return report, err
			}
			for i, column := range tableColumns[table] {
				if value, ok := values[i].([]byte); ok {
					values[i] = string(value)
				}
				if slices.Contains([]string{"topics", "trends", "hours", "value"}, column) {
					raw, ok := values[i].(string)
					if !ok {
						rows.Close()
						return report, fmt.Errorf("%s.%s is not JSON text", table, column)
					}
					var decoded any
					if err = json.Unmarshal([]byte(raw), &decoded); err != nil {
						rows.Close()
						return report, fmt.Errorf("invalid %s.%s JSON", table, column)
					}
					values[i] = decoded
				}
			}
			raw, err := json.Marshal(values)
			if err != nil {
				rows.Close()
				return report, err
			}
			hash.Write(raw)
			hash.Write([]byte{'\n'})
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return report, err
		}
		report.Tables[table] = TableDigest{Rows: count, SHA256: hex.EncodeToString(hash.Sum(nil))}
	}
	rows, err := db.Query("SELECT id,cycle,entered_at,type,repo_id FROM projects ORDER BY id")
	if err != nil {
		return report, err
	}
	repos, cycles := map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var p ProjectCheckpoint
		var kind, repo string
		if err = rows.Scan(&p.ID, &p.Cycle, &p.EnteredAt, &kind, &repo); err != nil {
			rows.Close()
			return report, err
		}
		report.Projects = append(report.Projects, p)
		report.ProjectTypes[kind]++
		repos[repo] = true
		cycles[p.Cycle] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, err
	}
	report.Repositories = len(repos)
	report.Cycles = len(cycles)
	rows, err = db.Query("SELECT day,hours FROM snapshot_days ORDER BY cycle,day")
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var day int64
		var raw string
		if err = rows.Scan(&day, &raw); err != nil {
			rows.Close()
			return report, err
		}
		var hours map[string]Point
		if err = json.Unmarshal([]byte(raw), &hours); err != nil {
			rows.Close()
			return report, err
		}
		for key, point := range hours {
			hour, err := strconv.ParseInt(key, 10, 64)
			if err != nil || floorDiv(point[0], Day) != day || floorDiv(point[0], Hour) != hour || point[1] < 0 {
				rows.Close()
				return report, errors.New("invalid UTC snapshot bucket or point")
			}
			report.SnapshotPoints++
			if report.Earliest == nil || point[0] < *report.Earliest {
				report.Earliest = ptr(point[0])
			}
			if report.Latest == nil || point[0] > *report.Latest {
				report.Latest = ptr(point[0])
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, err
	}
	rows, err = db.Query("SELECT key,value FROM system_state ORDER BY key")
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var key, raw string
		if err = rows.Scan(&key, &raw); err != nil {
			rows.Close()
			return report, err
		}
		var value any
		if err = json.Unmarshal([]byte(raw), &value); err != nil {
			rows.Close()
			return report, err
		}
		auditStateCheckpoints(report.StateCheckpoints, key, value)
	}
	err = rows.Err()
	rows.Close()
	return report, err
}
func auditStateCheckpoints(out map[string]any, path string, value any) {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			next := path + "." + key
			if slices.Contains([]string{"effectiveAt", "nextAt", "blockedUntil"}, key) {
				out[next] = child
			}
			auditStateCheckpoints(out, next, child)
		}
	case []any:
		for i, child := range value {
			auditStateCheckpoints(out, fmt.Sprintf("%s[%d]", path, i), child)
		}
	}
}
