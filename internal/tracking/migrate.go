package tracking

import (
	"fmt"
	"github.com/lizhian/github-trending-tracking/migrations"
)

// Migrate is offline: the directory lock excludes the application and backups.
func Migrate(path string) error {
	lock, err := directoryLock(path)
	if err != nil {
		return err
	}
	defer unlock(lock)
	db, err := database(path, "rw")
	if err != nil {
		return err
	}
	defer db.Close()
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version < 3 || version > SchemaVersion {
		return fmt.Errorf("unsupported migration source version %d", version)
	}
	if err = validateColumns(db); err != nil {
		return err
	}
	if err = CheckDatabase(db); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, name := range MigrationNames[version:] {
		source, err := migrations.Files.ReadFile(name)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(string(source)); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", SchemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}
