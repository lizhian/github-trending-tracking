package tracking

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/lizhian/github-trending-tracking/migrations"
	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

const SchemaVersion = 3
const DatabaseName = "tracking.sqlite"

var MigrationNames = []string{"0001_tracking.sql", "0002_free_tier.sql", "0003_tracking_types.sql"}

type Store struct {
	DB        *sql.DB
	lock      *os.File
	closeOnce sync.Once
}

func database(path, mode string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	q.Set("mode", mode)
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	// A single connection also keeps explicit transactions and per-connection PRAGMAs simple.
	db.SetMaxOpenConns(1)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func directoryLock(path string) (*os.File, error) {
	info, err := os.Stat(filepath.Dir(path))
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("data directory must already exist: %s", filepath.Dir(path))
	}
	f, err := os.OpenFile(filepath.Join(filepath.Dir(path), ".writer.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("data directory already has an active writer")
	}
	return f, nil
}
func unlock(f *os.File) {
	if f != nil {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}
}

// Open never creates a missing database. Initialization/import is an explicit offline action.
func Open(path string) (*Store, error) {
	lock, err := directoryLock(path)
	if err != nil {
		return nil, err
	}
	db, err := database(path, "rw")
	if err != nil {
		unlock(lock)
		return nil, fmt.Errorf("open existing database: %w", err)
	}
	s := &Store{DB: db, lock: lock}
	fail := func(err error) (*Store, error) { s.Close(); return nil, err }
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if version != SchemaVersion {
		return fail(fmt.Errorf("unsupported database schema version %d (want %d)", version, SchemaVersion))
	}
	if err = validateColumns(db); err != nil {
		return fail(err)
	}
	var journal string
	if err = db.QueryRow("PRAGMA journal_mode=WAL").Scan(&journal); err != nil {
		return fail(err)
	}
	if journal != "wal" {
		return fail(errors.New("SQLite WAL is required"))
	}
	if err = CheckDatabase(db); err != nil {
		return fail(err)
	}
	// Fail at startup if the mounted database cannot actually be written.
	tx, err := db.Begin()
	if err != nil {
		return fail(err)
	}
	if _, err = tx.Exec("UPDATE system_state SET value=value WHERE 0"); err != nil {
		tx.Rollback()
		return fail(err)
	}
	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	return s, nil
}
func (s *Store) Close() error {
	var err error
	s.closeOnce.Do(func() { err = s.DB.Close(); unlock(s.lock) })
	return err
}

func initializeDB(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, name := range MigrationNames {
		b, err := migrations.Files.ReadFile(name)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(string(b)); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("PRAGMA user_version=3"); err != nil {
		return err
	}
	return tx.Commit()
}
func Initialize(path string) error {
	lock, err := directoryLock(path)
	if err != nil {
		return err
	}
	defer unlock(lock)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	f.Close()
	ok := false
	defer func() {
		if !ok {
			os.Remove(path)
		}
	}()
	db, err := database(path, "rw")
	if err != nil {
		return err
	}
	defer db.Close()
	if err = initializeDB(db); err != nil {
		return err
	}
	ok = true
	return nil
}

var tableColumns = map[string][]string{
	"projects":      {"id", "cycle", "name", "description", "language", "topics", "entered_at", "listed_at", "observed_at", "attempted_at", "error", "stars", "trends", "metadata_at", "type", "repo_id"},
	"snapshot_days": {"cycle", "day", "hours"},
	"system_state":  {"key", "value"},
}

func validateColumns(db *sql.DB) error {
	for table, want := range tableColumns {
		rows, err := db.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			return err
		}
		got := []string{}
		for rows.Next() {
			var cid, notnull, pk int
			var name, kind string
			var def any
			if err = rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
				rows.Close()
				return err
			}
			got = append(got, name)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if !slices.Equal(got, want) {
			return fmt.Errorf("unexpected %s schema columns: %v", table, got)
		}
	}
	return nil
}
func CheckDatabase(db *sql.DB) error {
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("SQLite integrity check failed: %s", result)
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("SQLite foreign key check failed")
	}
	return rows.Err()
}

func (s *Store) State(key string, out any) (bool, error) {
	var raw string
	err := s.DB.QueryRow("SELECT value FROM system_state WHERE key=?", key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), out)
}
func (s *Store) SetState(key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec("INSERT INTO system_state(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, string(b))
	return err
}
func (s *Store) Policy() (*SamplingPolicy, error) {
	var policy SamplingPolicy
	ok, err := s.State("sampling_policy", &policy)
	if err != nil || !ok {
		return nil, err
	}
	return &policy, nil
}

type ProjectRow struct {
	ID, Cycle, Name                              string
	Description, Language                        *string
	Topics                                       string
	EnteredAt, ListedAt, ObservedAt, AttemptedAt int64
	Error                                        *string
	Stars                                        int64
	Trends                                       string
	MetadataAt                                   int64
	Type, RepoID                                 string
}

const projectColumns = "id,cycle,name,description,language,topics,entered_at,listed_at,observed_at,attempted_at,error,stars,trends,metadata_at,type,repo_id"

func scanProject(row interface{ Scan(...any) error }) (p ProjectRow, err error) {
	err = row.Scan(&p.ID, &p.Cycle, &p.Name, &p.Description, &p.Language, &p.Topics, &p.EnteredAt, &p.ListedAt, &p.ObservedAt, &p.AttemptedAt, &p.Error, &p.Stars, &p.Trends, &p.MetadataAt, &p.Type, &p.RepoID)
	return
}
func (s *Store) ProjectRows(where string, args ...any) ([]ProjectRow, error) {
	rows, err := s.DB.Query("SELECT "+projectColumns+" FROM projects "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ProjectRow{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}
func (s *Store) Histories(cycles []string, now int64) (map[string][]Point, error) {
	output := map[string][]Point{}
	for start := 0; start < len(cycles); start += 90 {
		group := cycles[start:min(start+90, len(cycles))]
		args := make([]any, 0, len(group)+1)
		for _, cycle := range group {
			args = append(args, cycle)
		}
		args = append(args, (now-Retention)/Day)
		rows, err := s.DB.Query("SELECT cycle,hours FROM snapshot_days WHERE cycle IN ("+strings.TrimSuffix(strings.Repeat("?,", len(group)), ",")+") AND day>=? ORDER BY day", args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var cycle, raw string
			if err = rows.Scan(&cycle, &raw); err != nil {
				rows.Close()
				return nil, err
			}
			var hours map[string]Point
			if err = json.Unmarshal([]byte(raw), &hours); err != nil {
				rows.Close()
				return nil, err
			}
			for _, p := range hours {
				if p[0] >= now-Retention && p[0] <= now {
					output[cycle] = append(output[cycle], p)
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	for _, points := range output {
		slices.SortFunc(points, func(a, b Point) int {
			if a[0] < b[0] {
				return -1
			}
			if a[0] > b[0] {
				return 1
			}
			return 0
		})
	}
	return output, nil
}
