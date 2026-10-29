package tracking

// 本文件负责 SQLite 一致快照、离线恢复和每日备份判定。
// 先读 Backup/Restore 的发布流程，再读 BackupRunner 如何依据北京时间去重。

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

// Go 时间格式用参考时间 2006-01-02 15:04:05 的对应数字，而非 Java 的 yyyyMMdd/HHmmss。
// 这里固定格式写 UTC 时间，末尾 Z 是布局中的字面字符，调用方先转 UTC。
const backupLayout = "20060102T150405.000Z"
const backupPrefix = "backup-"

// BackupResult 记录已发布备份的路径、UTC 时间和审计结果，供命令行与每日维护循环使用。
type BackupResult struct {
	Path  string        `json:"path"`
	At    time.Time     `json:"at"`
	Audit DatabaseAudit `json:"audit"`
}

// databaseFile 通过 SQLite 获取主数据库实际文件路径，而不是依赖启动时的相对路径。
func databaseFile(db *sql.DB) (string, error) {
	var sequence int
	var name, path string
	if err := db.QueryRow("PRAGMA database_list").Scan(&sequence, &name, &path); err != nil {
		return "", err
	}
	return path, nil
}

// resolvedDirectory 将目录转为绝对路径并解析符号链接，再确认目标确实是已存在的目录。
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

// withinDirectory 用相对路径判断 child 是否等于或位于 parent 内，避免直接比较字符串前缀误判。
func withinDirectory(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

// backupDirectory 验证备份目录与活动数据目录彼此独立，连互相包含也不允许。
// 先解析符号链接，防止同一目录通过两个不同路径绕过校验。
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

// auditSnapshot 只读打开独立快照，检查受支持的版本、列结构和全部业务数据。
// 旧 schema 3–5 可审计恢复，但仍需迁移到当前版本才能用于服务运行。
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
	if version < 3 || version > SchemaVersion {
		return DatabaseAudit{}, fmt.Errorf("unsupported backup schema version %d", version)
	}
	if err = validateColumns(db); err != nil {
		return DatabaseAudit{}, err
	}
	return Audit(db)
}

// syncFile 将文件内容同步到存储设备；Close 释放句柄，Sync 用于要求持久化，两者职责不同。
func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

// syncDirectory 同步目录元数据，配合快照发布/删除降低崩溃后目录项丢失的风险。
func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// backupTime 解析备份文件名中的时间；bool 为 false 表示不是可识别的备份名，不应参与轮转。
func backupTime(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, backupPrefix) || !strings.HasSuffix(name, ".sqlite") {
		return time.Time{}, false
	}
	stamp := strings.TrimSuffix(strings.TrimPrefix(name, backupPrefix), ".sqlite")
	at, err := time.Parse(backupLayout, stamp)
	return at, err == nil
}

// Backup 请 SQLite 用 VACUUM INTO 创建包含已提交 WAL 内容的一致快照，而非复制活动主文件。
// 流程为临时文件 → 审计 → 同步 → 发布 → 清理 72 小时前旧备份；新备份失败不先删旧副本。
// 返回命名结果与错误；若发布后清理失败，result 仍可能指向有效的新备份。
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
	// 用硬链接将已验证临时文件发布到最终名称；目标已存在会失败，不覆盖旧副本。
	// 临时文件与最终文件位于同一目录/文件系统，defer 删除临时名后最终链接仍保留内容。
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

// Restore 离线恢复独立快照，拒绝已有目标和带 WAL/SHM/journal 的源文件。
// 复制到临时文件并同步，比较完整审计后再发布；保留原库与源备份供检查。
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
	// 审计包含 map、slice 等不可直接用 == 比较的成员，因此用 DeepEqual 比较整个报告内容。
	if !reflect.DeepEqual(sourceAudit, restoredAudit) {
		return errors.New("restored database differs from backup")
	}
	if err = os.Link(stagePath, target); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(target))
}

// BackupRunner 跟踪每日备份时段；不写业务状态，而从已验证快照文件名识别重启前的完成记录。
// lastSlot 是进程内缓存；该类型由单个维护循环串行调用。
type BackupRunner struct {
	store        *Store
	directory    string
	hour, minute int
	lastSlot     int64
}

// NewBackupRunner 验证固定北京时间 HH:MM 与独立目录，构造尚未处理任何时段的备份执行器。
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

// slot 返回不晚于给定时间的最近每日备份时段，以 Unix 毫秒表示，固定按 UTC+8 计算。
func (r *BackupRunner) slot(at time.Time) int64 {
	now := at.UnixMilli()
	day := floorDiv(now+8*Hour, Day)*Day - 8*Hour
	slot := day + int64(r.hour)*Hour + int64(r.minute)*60_000
	if slot > now {
		slot -= Day
	}
	return slot
}

// Tick 检查最近每日时段是否已有有效快照；有则返回 (nil, nil)，否则创建备份。
// 失败不推进 lastSlot，允许下一分钟重试；重启后先扫描并验证文件，避免重复备份。
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
		// 同名格式文件还必须审计成功才算已完成备份；失败文件不会挡住新尝试。
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

// removeSQLite 清理指定临时数据库及其伴随文件；只用于本流程拥有的临时路径，忽略删除错误。
func removeSQLite(path string) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_ = os.Remove(path + suffix)
	}
}
