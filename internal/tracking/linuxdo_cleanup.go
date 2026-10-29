package tracking

// 本文件对启用 README 准入前的 Linux.do 存量执行一次性清洗。
// 先备份并冻结目标追踪周期，再分批处理；进度持久化以支持重启续作。

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"
)

// linuxdoCleanupItem 冻结清洗目标的项目 ID、追踪周期和仓库 ID，防止处理结果误删后来替代的周期。
type linuxdoCleanupItem struct {
	ID        string `json:"id"`
	Cycle     string `json:"cycle"`
	RepoID    string `json:"repoId"`
	Name      string `json:"name"`
	Status    string `json:"status"` // 未确认时为空；明确确认后为 kept 或 removed
	Reason    string `json:"reason,omitempty"`
	CheckedAt int64  `json:"checkedAt,omitempty"`
	NextAt    int64  `json:"nextAt,omitempty"`
	Error     string `json:"error,omitempty"`
}

// linuxdoCleanupState 保存一次性清洗备份、固定目标和逐项进度；CompletedAt=0 表示尚未全部得到明确结果。
type linuxdoCleanupState struct {
	StartedAt   int64                `json:"startedAt"`
	CompletedAt int64                `json:"completedAt"`
	Backup      string               `json:"backup"`
	Items       []linuxdoCleanupItem `json:"items"`
}

// linuxdoGate 返回是否允许普通采集/清理；有未清洗存量时关闭门禁，无存量则直接记录完成。
// 维护模式先做隔离备份再分批清洗，完成后仍保持暂停，须重启普通模式才能恢复。
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
			// 子目录与每日备份文件分开，使清洗前快照不进入日常顶层轮转。
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

// logCleanupPaused 只在首次发现清洗阻塞时记录提示，避免每分钟刷相同日志。
func (s *Scheduler) logCleanupPaused() {
	if s.cleanupLog == "" {
		log.Print("linux.do cleanup required: collection and retention paused; restart serve --linuxdo-cleanup")
		s.cleanupLog = "paused"
	}
}

// cleanLinuxdo 每轮最多处理 20 个到期目标，请求失败保留项目并设下次重试。
// 明确结果的缓存、删除和进度同事务提交，并用冻结身份校验目标；全部完成才写完成时间。
func (s *Scheduler) cleanLinuxdo(ctx context.Context, progress *linuxdoCleanupState) error {
	attempted := 0
	for i := range progress.Items {
		// 通过元素地址原地更新进度；若写成 for _, item := range ...，修改结构体副本不会更新 slice 元素。
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
		// 资格、删除、不符合缓存和逐项进度必须一起提交；任一失败则整体回滚。
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
