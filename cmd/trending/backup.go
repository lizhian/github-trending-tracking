package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/lizhian/github-trending-tracking/internal/tracking"
)

func backupCommand(command string, args []string) (bool, error) {
	if command != "backup" && command != "restore" {
		return false, nil
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	path := flags.String("db", env("DATA_DIR", "data")+"/"+tracking.DatabaseName, "active database for backup; nonexistent target database for restore")
	directory := flags.String("backup-dir", env("BACKUP_DIR", ""), "existing backup directory outside active data directory")
	source := flags.String("source", "", "standalone validated SQLite backup to restore")
	if err := flags.Parse(args); err != nil {
		return true, err
	}
	if flags.NArg() != 0 {
		return true, errors.New("unexpected arguments")
	}
	if command == "restore" {
		if *source == "" {
			return true, errors.New("restore requires --source")
		}
		if err := tracking.Restore(*source, *path); err != nil {
			return true, err
		}
		fmt.Println("restore validated; original files preserved; startup waits for next collection slot")
		return true, nil
	}
	store, err := tracking.Open(*path)
	if err != nil {
		return true, err
	}
	defer store.Close()
	result, err := store.Backup(*directory, time.Now())
	if err != nil {
		return true, err
	}
	fmt.Printf("backup validated: %s projects=%d snapshotPoints=%d\n", result.Path, result.Audit.Tables["projects"].Rows, result.Audit.SnapshotPoints)
	return true, nil
}

// Run a separate daily-maintenance loop so restart's collection gate never
// postpones the backup. Store's single SQLite connection serializes DB access.
func startBackupLoop(ctx context.Context, store *tracking.Store) (func(), error) {
	runner, err := tracking.NewBackupRunner(store, env("BACKUP_DIR", ""), env("BACKUP_TIME", "03:00"))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		run := func() {
			result, err := runner.Tick(time.Now())
			if err != nil {
				log.Printf("daily backup failed: %v", err)
			} else if result != nil {
				log.Printf("daily backup validated: %s", result.Path)
			}
		}
		run()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}
