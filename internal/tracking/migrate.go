package tracking

// 本文件负责停机状态下的数据库升级。SQL 来自 migrations 包的嵌入文件；
// 先检查旧库，再在同一事务内更新表结构和版本，失败时一起回滚。

import (
	"fmt"
	"github.com/lizhian/github-trending-tracking/migrations"
)

// Migrate 离线升级已有数据库；目录锁排除正在运行的应用和独立备份进程。
// 只执行当前版本之后的迁移，表结构与 user_version 同事务提交，失败则一起回滚；不会创建缺失库。
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
