package tracking

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
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
type ImportReport struct {
	At                        string        `json:"at"`
	ExportSHA256              string        `json:"exportSHA256"`
	Migrations                []string      `json:"migrations"`
	Source                    DatabaseAudit `json:"source"`
	Target                    DatabaseAudit `json:"target"`
	Matched                   bool          `json:"matched"`
	LeaseCleared              bool          `json:"leaseCleared"`
	LeaseSHA256               string        `json:"leaseSHA256,omitempty"`
	OldWriterStopped          bool          `json:"oldWriterStopped"`
	ComparedSourceSystemState *TableDigest  `json:"comparedSourceSystemState,omitempty"`
}

// Audit checks the entire physical dataset, including history older than the
// query horizon. JSON text is hashed by parsed content rather than key order.
// ignoreLease is only for the explicitly acknowledged D1 cutover exception.
func Audit(db *sql.DB, ignoreLease bool) (DatabaseAudit, error) {
	report := DatabaseAudit{Tables: map[string]TableDigest{}, ProjectTypes: map[string]int{}, Projects: []ProjectCheckpoint{}, StateCheckpoints: map[string]any{}}
	if err := CheckDatabase(db); err != nil {
		return report, err
	}
	report.Integrity = "ok"
	report.ForeignKeys = "ok"
	for _, table := range []string{"projects", "snapshot_days", "system_state"} {
		query := "SELECT " + strings.Join(tableColumns[table], ",") + " FROM " + table
		if table == "system_state" && ignoreLease {
			query += " WHERE key!='lease'"
		}
		switch table {
		case "projects":
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
		if ignoreLease && key == "lease" {
			continue
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

// ImportD1 stages a trusted, frozen Wrangler SQL export, validates its migrations,
// and publishes only the three business tables. It never overwrites a target.
func ImportD1(sourcePath, targetPath, reportPath string, oldWriterStopped bool) (report ImportReport, err error) {
	report.At = time.Now().UTC().Format(time.RFC3339)
	report.OldWriterStopped = oldWriterStopped
	if reportPath == "" {
		return report, errors.New("a private migration report path is required")
	}
	absSource, _ := filepath.Abs(sourcePath)
	absTarget, _ := filepath.Abs(targetPath)
	absReport, _ := filepath.Abs(reportPath)
	if absSource == absTarget || absSource == absReport || absTarget == absReport {
		return report, errors.New("source, target and report must be separate files")
	}
	if _, err = os.Lstat(targetPath); !os.IsNotExist(err) {
		return report, errors.New("target already exists or is inaccessible")
	}
	lock, err := directoryLock(targetPath)
	if err != nil {
		return report, err
	}
	defer unlock(lock)
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		return report, err
	}
	digest := sha256.Sum256(raw)
	report.ExportSHA256 = hex.EncodeToString(digest[:])
	sourceFile, err := os.CreateTemp(filepath.Dir(targetPath), ".d1-source-*.sqlite")
	if err != nil {
		return report, err
	}
	sourceName := sourceFile.Name()
	sourceFile.Close()
	defer removeSQLite(sourceName)
	source, err := database(sourceName, "rw")
	if err != nil {
		return report, err
	}
	defer source.Close()
	if _, err = source.Exec(string(raw)); err != nil {
		return report, fmt.Errorf("invalid D1 SQL export: %w", err)
	}
	if err = validateColumns(source); err != nil {
		return report, err
	}
	versions, err := source.Query("SELECT name FROM d1_migrations ORDER BY name")
	if err != nil {
		return report, errors.New("export must include d1_migrations to verify version")
	}
	for versions.Next() {
		var name string
		if err = versions.Scan(&name); err != nil {
			versions.Close()
			return report, err
		}
		report.Migrations = append(report.Migrations, name)
	}
	err = versions.Err()
	versions.Close()
	if err != nil {
		return report, err
	}
	if !slices.Equal(report.Migrations, MigrationNames) {
		return report, fmt.Errorf("unexpected D1 migration versions: %v", report.Migrations)
	}
	var lease string
	err = source.QueryRow("SELECT value FROM system_state WHERE key='lease'").Scan(&lease)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return report, err
	}
	if err == nil {
		if !oldWriterStopped {
			return report, errors.New("legacy lease present: stop old writer and acknowledge before import")
		}
		report.LeaseCleared = true
		hash := sha256.Sum256([]byte(lease))
		report.LeaseSHA256 = hex.EncodeToString(hash[:])
	}
	report.Source, err = Audit(source, false)
	if err != nil {
		return report, err
	}
	stageFile, err := os.CreateTemp(filepath.Dir(targetPath), ".import-target-*.sqlite")
	if err != nil {
		return report, err
	}
	stageName := stageFile.Name()
	stageFile.Close()
	defer removeSQLite(stageName)
	target, err := database(stageName, "rw")
	if err != nil {
		return report, err
	}
	defer target.Close()
	if err = initializeDB(target); err != nil {
		return report, err
	}
	tx, err := target.Begin()
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	for _, table := range []string{"projects", "snapshot_days", "system_state"} {
		columns := tableColumns[table]
		query := "SELECT " + strings.Join(columns, ",") + " FROM " + table
		if table == "system_state" && report.LeaseCleared {
			query += " WHERE key!='lease'"
		}
		rows, e := source.Query(query)
		if e != nil {
			return report, e
		}
		statement, e := tx.Prepare("INSERT INTO " + table + " (" + strings.Join(columns, ",") + ") VALUES(" + strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",") + ")")
		if e != nil {
			rows.Close()
			return report, e
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if e = rows.Scan(pointers...); e != nil {
				break
			}
			if _, e = statement.Exec(values...); e != nil {
				break
			}
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		statement.Close()
		if e != nil {
			return report, e
		}
	}
	if err = tx.Commit(); err != nil {
		return report, err
	}
	report.Target, err = Audit(target, false)
	if err != nil {
		return report, err
	}
	comparedSource := report.Source
	if report.LeaseCleared {
		comparedSource, err = Audit(source, true)
		if err != nil {
			return report, err
		}
		digest := comparedSource.Tables["system_state"]
		report.ComparedSourceSystemState = &digest
	}
	report.Matched = reflect.DeepEqual(comparedSource, report.Target)
	if !report.Matched {
		return report, errors.New("source/target migration audits differ")
	}
	if _, err = target.Exec("PRAGMA journal_mode=DELETE"); err != nil {
		return report, err
	}
	if err = target.Close(); err != nil {
		return report, err
	}
	// Preserve the evidence before publishing the database. Report contains state
	// checkpoints, so both it and the export belong outside the public repository.
	reportBytes, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return report, err
	}
	if err = writePrivateFile(reportPath, append(reportBytes, '\n')); err != nil {
		return report, err
	}
	file, err := os.OpenFile(stageName, os.O_RDWR, 0)
	if err != nil {
		return report, err
	}
	err = file.Sync()
	file.Close()
	if err != nil {
		return report, err
	}
	// Link is atomic and refuses to replace a file created by another process.
	if err = os.Link(stageName, targetPath); err != nil {
		return report, err
	}
	return report, nil
}
func removeSQLite(path string) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_ = os.Remove(path + suffix)
	}
}
func writePrivateFile(path string, body []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".migration-report-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
