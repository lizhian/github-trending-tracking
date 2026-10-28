package tracking

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const backupLayout = "20060102T150405.000Z"
const backupPrefix = "backup-"

type BackupResult struct {
	Path  string        `json:"path"`
	At    time.Time     `json:"at"`
	Audit DatabaseAudit `json:"audit"`
}

func databaseFile(db *sql.DB) (string, error) {
	var sequence int
	var name, path string
	if err := db.QueryRow("PRAGMA database_list").Scan(&sequence, &name, &path); err != nil {
		return "", err
	}
	return path, nil
}
func resolvedDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("backup directory is not a directory")
	}
	return resolved, nil
}
func withinDirectory(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
func (s *Store) backupDirectory(path string) (string, error) {
	if path == "" {
		return "", errors.New("BACKUP_DIR must name an existing directory outside the database directory")
	}
	directory, err := resolvedDirectory(path)
	if err != nil {
		return "", fmt.Errorf("backup directory unavailable: %w", err)
	}
	database, err := databaseFile(s.DB)
	if err != nil {
		return "", err
	}
	dataDirectory, err := resolvedDirectory(filepath.Dir(database))
	if err != nil {
		return "", err
	}
	if withinDirectory(dataDirectory, directory) || withinDirectory(directory, dataDirectory) {
		return "", errors.New("backup and active database directories must be separate")
	}
	return directory, nil
}
func auditSnapshot(path string) (DatabaseAudit, error) {
	db, err := database(path, "ro")
	if err != nil {
		return DatabaseAudit{}, err
	}
	defer db.Close()
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return DatabaseAudit{}, err
	}
	if version != SchemaVersion {
		return DatabaseAudit{}, fmt.Errorf("unsupported backup schema version %d", version)
	}
	if err = validateColumns(db); err != nil {
		return DatabaseAudit{}, err
	}
	return Audit(db, false)
}
func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
func backupTime(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, backupPrefix) || !strings.HasSuffix(name, ".sqlite") {
		return time.Time{}, false
	}
	stamp := strings.TrimSuffix(strings.TrimPrefix(name, backupPrefix), ".sqlite")
	at, err := time.Parse(backupLayout, stamp)
	return at, err == nil
}

// Backup asks SQLite to create one transactionally consistent standalone
// snapshot, including committed WAL contents. Old snapshots are pruned only
// after the new snapshot has passed schema, integrity and business-data audit.
func (s *Store) Backup(backupDir string, now time.Time) (result BackupResult, err error) {
	directory, err := s.backupDirectory(backupDir)
	if err != nil {
		return result, err
	}
	temporary, err := os.CreateTemp(directory, ".backup-*.partial")
	if err != nil {
		return result, err
	}
	stage := temporary.Name()
	temporary.Close()
	os.Remove(stage)
	defer removeSQLite(stage)
	if _, err = s.DB.Exec("VACUUM main INTO ?", stage); err != nil {
		return result, fmt.Errorf("SQLite backup snapshot failed: %w", err)
	}
	if err = os.Chmod(stage, 0600); err != nil {
		return result, err
	}
	audit, err := auditSnapshot(stage)
	if err != nil {
		return result, fmt.Errorf("backup validation failed: %w", err)
	}
	if err = syncFile(stage); err != nil {
		return result, err
	}
	result = BackupResult{Path: filepath.Join(directory, backupPrefix+now.UTC().Format(backupLayout)+".sqlite"), At: now.UTC(), Audit: audit}
	if err = os.Link(stage, result.Path); err != nil {
		return result, fmt.Errorf("publish backup: %w", err)
	}
	if err = syncDirectory(directory); err != nil {
		return result, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return result, err
	}
	cutoff := now.Add(-72 * time.Hour)
	for _, entry := range entries {
		at, ok := backupTime(entry.Name())
		if !ok || entry.IsDir() || at.After(cutoff) || filepath.Join(directory, entry.Name()) == result.Path {
			continue
		}
		if err = os.Remove(filepath.Join(directory, entry.Name())); err != nil {
			return result, fmt.Errorf("new backup is valid, but removing expired backup failed: %w", err)
		}
	}
	if err = syncDirectory(directory); err != nil {
		return result, err
	}
	return result, nil
}

// Restore only consumes a standalone snapshot. The application must be stopped,
// and the target must not exist; keep any damaged directory for investigation.
func Restore(snapshot, target string) error {
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		return errors.New("restore target already exists or is inaccessible")
	}
	lock, err := directoryLock(target)
	if err != nil {
		return err
	}
	defer unlock(lock)
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err = os.Lstat(snapshot + suffix); !os.IsNotExist(err) {
			return errors.New("restore requires a standalone backup without active SQLite sidecars")
		}
	}
	sourceAudit, err := auditSnapshot(snapshot)
	if err != nil {
		return fmt.Errorf("invalid restore backup: %w", err)
	}
	source, err := os.Open(snapshot)
	if err != nil {
		return err
	}
	defer source.Close()
	stage, err := os.CreateTemp(filepath.Dir(target), ".restore-*.partial")
	if err != nil {
		return err
	}
	stagePath := stage.Name()
	defer removeSQLite(stagePath)
	if _, err = io.Copy(stage, source); err != nil {
		stage.Close()
		return err
	}
	if err = stage.Sync(); err != nil {
		stage.Close()
		return err
	}
	if err = stage.Close(); err != nil {
		return err
	}
	restoredAudit, err := auditSnapshot(stagePath)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(sourceAudit, restoredAudit) {
		return errors.New("restored database differs from backup")
	}
	if err = os.Link(stagePath, target); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(target))
}

// BackupRunner records no business state: immutable successful snapshot names
// carry their time across restart, independently of the collection startup gate.
type BackupRunner struct {
	store        *Store
	directory    string
	hour, minute int
	lastSlot     int64
}

func NewBackupRunner(store *Store, directory, beijingTime string) (*BackupRunner, error) {
	parsed, err := time.Parse("15:04", beijingTime)
	if err != nil || parsed.Format("15:04") != beijingTime {
		return nil, errors.New("BACKUP_TIME must be Beijing HH:MM")
	}
	resolved, err := store.backupDirectory(directory)
	if err != nil {
		return nil, err
	}
	return &BackupRunner{store: store, directory: resolved, hour: parsed.Hour(), minute: parsed.Minute(), lastSlot: -1}, nil
}
func (r *BackupRunner) slot(at time.Time) int64 {
	now := at.UnixMilli()
	day := floorDiv(now+8*Hour, Day)*Day - 8*Hour
	slot := day + int64(r.hour)*Hour + int64(r.minute)*60_000
	if slot > now {
		slot -= Day
	}
	return slot
}

// Tick returns nil when the latest daily slot already has a validated snapshot.
// A failed attempt leaves lastSlot unchanged, so the next minute may retry.
func (r *BackupRunner) Tick(now time.Time) (*BackupResult, error) {
	due := r.slot(now)
	if r.lastSlot >= due {
		return nil, nil
	}
	entries, err := os.ReadDir(r.directory)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		at, ok := backupTime(entry.Name())
		if !ok || entry.IsDir() || r.slot(at) != due || at.After(now) {
			continue
		}
		if _, err = auditSnapshot(filepath.Join(r.directory, entry.Name())); err == nil {
			r.lastSlot = due
			return nil, nil
		}
	}
	result, err := r.store.Backup(r.directory, now)
	if err != nil {
		return nil, err
	}
	r.lastSlot = due
	return &result, nil
}
