package main

// 本文件连接命令行与备份功能，并启动独立的每日备份 goroutine。
// 实际快照和恢复逻辑在 internal/tracking/backup.go；它与采集调度分开计时。

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/lizhian/github-trending-tracking/internal/tracking"
)

// backupCommand 尝试处理 backup/restore；第一个返回值表示是否识别此命令，第二个表示执行错误。
// (false, nil) 是“交给其他分支”，并不是备份成功；恢复要求目标库尚不存在。
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

// startBackupLoop 创建独立的每日维护循环，避免重启后的采集等待推迟备份。
// 返回的 func() 是清理函数：取消子 context 并等待 goroutine 退出；调用者应 defer 它。
// 多个任务共享 Store，但它的单 SQLite 连接会串行执行数据库访问。
func startBackupLoop(ctx context.Context, store *tracking.Store) (func(), error) {
	runner, err := tracking.NewBackupRunner(store, env("BACKUP_DIR", ""), env("BACKUP_TIME", "03:00"))
	if err != nil {
		return nil, err
	}
	// 右侧使用传入的父 ctx；:= 复用同作用域的 ctx，只新声明 cancel，之后 ctx 指向派生上下文。
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		// defer 保证 goroutine 任一路径正常返回都会关闭完成通知，调用者才不会一直等。
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
			// select 等待多个 channel：取消与定时器任一就绪即可继续；多个同时就绪时不保证优先选取消。
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
