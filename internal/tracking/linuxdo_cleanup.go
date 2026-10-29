package tracking

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"
)

type linuxdoCleanupItem struct {
	ID        string `json:"id"`
	Cycle     string `json:"cycle"`
	RepoID    string `json:"repoId"`
	Name      string `json:"name"`
	Status    string `json:"status"` // empty until confirmed; kept or removed afterwards
	Reason    string `json:"reason,omitempty"`
	CheckedAt int64  `json:"checkedAt,omitempty"`
	NextAt    int64  `json:"nextAt,omitempty"`
	Error     string `json:"error,omitempty"`
}

type linuxdoCleanupState struct {
	StartedAt   int64                `json:"startedAt"`
	CompletedAt int64                `json:"completedAt"`
	Backup      string               `json:"backup"`
	Items       []linuxdoCleanupItem `json:"items"`
}

func (s *Scheduler) linuxdoGate(ctx context.Context) (bool, error) {
	var progress linuxdoCleanupState
	found, err := s.store.State(linuxdoCleanupKey, &progress)
	if err != nil {
		return false, err
	}
	if !found {
		rows, err := s.store.ProjectRows("WHERE type='linux.do' ORDER BY id")
		if err != nil {
			return false, err
		}
		if len(rows) > 0 && !s.options.LinuxdoCleanup {
			s.logCleanupPaused()
			return false, nil
		}
		progress = linuxdoCleanupState{StartedAt: s.upstream.clock.Now(), Items: []linuxdoCleanupItem{}}
		if len(rows) == 0 {
			progress.CompletedAt = s.upstream.clock.Now()
		} else {
			if s.options.BackupDir == "" {
				return false, errors.New("Linux.do cleanup requires BACKUP_DIR")
			}
			// This subdirectory is outside the daily backup runner's rotation.
			directory := filepath.Join(s.options.BackupDir, "linuxdo-cleanup-v1")
			if err = os.MkdirAll(directory, 0700); err != nil {
				return false, err
			}
			backup, err := s.store.Backup(directory, time.UnixMilli(s.upstream.clock.Now()))
			if err != nil {
				return false, err
			}
			progress.Backup = backup.Path
			for _, row := range rows {
				progress.Items = append(progress.Items, linuxdoCleanupItem{ID: row.ID, Cycle: row.Cycle, RepoID: row.RepoID, Name: row.Name})
			}
		}
		if err = s.store.SetState(linuxdoCleanupKey, progress); err != nil {
			return false, err
		}
	}
	if !s.options.LinuxdoCleanup {
		if progress.CompletedAt == 0 {
			s.logCleanupPaused()
			return false, nil
		}
		return true, nil
	}
	if progress.CompletedAt == 0 {
		if err = s.cleanLinuxdo(ctx, &progress); err != nil {
			return false, err
		}
	}
	kept, removed := 0, 0
	for _, item := range progress.Items {
		if item.Status == "kept" {
			kept++
		} else if item.Status == "removed" {
			removed++
		}
	}
	message := jsonText(progress)
	if message != s.cleanupLog {
		log.Printf("linux.do cleanup: kept=%d removed=%d pending=%d complete=%t backup=%s; collection remains paused", kept, removed, len(progress.Items)-kept-removed, progress.CompletedAt != 0, progress.Backup)
		s.cleanupLog = message
	}
	return false, nil
}

func (s *Scheduler) logCleanupPaused() {
	if s.cleanupLog == "" {
		log.Print("linux.do cleanup required: collection and retention paused; restart serve --linuxdo-cleanup")
		s.cleanupLog = "paused"
	}
}

func (s *Scheduler) cleanLinuxdo(ctx context.Context, progress *linuxdoCleanupState) error {
	attempted := 0
	for i := range progress.Items {
		item := &progress.Items[i]
		if item.Status != "" || item.NextAt > s.upstream.clock.Now() || attempted >= 20 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		attempted++
		result, requestErr := s.linuxdoReadme(ctx, item.RepoID)
		if err := ctx.Err(); err != nil {
			return err
		}
		if requestErr != nil {
			item.NextAt, item.Error = s.readmeRetryAt(), safeError(requestErr)
		} else {
			item.CheckedAt, item.Reason = result.CheckedAt, result.Reason
			item.NextAt, item.Error = 0, ""
			item.Status = "kept"
			if !result.Eligible {
				item.Status = "removed"
			}
		}
		tx, err := s.store.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		// Keep qualification, deletion, negative cache and checkpoint atomic.
		if requestErr == nil {
			var exists int
			err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects WHERE type='linux.do' AND id=? AND cycle=? AND repo_id=?", item.ID, item.Cycle, item.RepoID).Scan(&exists)
			if err == nil && exists != 1 {
				err = errors.New("Linux.do cleanup target changed")
			}
			if err == nil && !result.Eligible {
				_, err = tx.ExecContext(ctx, "DELETE FROM projects WHERE type='linux.do' AND id=? AND cycle=? AND repo_id=?", item.ID, item.Cycle, item.RepoID)
				if err == nil {
					err = setTxState(ctx, tx, readmeCacheKey(item.RepoID), result)
				}
			}
		}
		if err == nil {
			err = setTxState(ctx, tx, linuxdoCleanupKey, progress)
		}
		if err == nil {
			err = setTxState(ctx, tx, "upstream", s.upstream.hosts)
		}
		if err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	for _, item := range progress.Items {
		if item.Status == "" {
			return nil
		}
	}
	progress.CompletedAt = s.upstream.clock.Now()
	return s.store.SetState(linuxdoCleanupKey, progress)
}
