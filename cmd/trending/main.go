// Package main 是 trending 可执行程序的入口；Go 以 main 包的 main 函数启动程序，
// 不需要 Java 的 public static void main(String[] args)。
// 建议阅读顺序：main → run 的 serve 分支 → tracking.Open → tracking.Handler → Scheduler.Run。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lizhian/github-trending-tracking/internal/tracking"
)

// 包级变量在本包内可见；发布构建可用链接器 -X 注入版本信息，本地默认 dev/unknown。
var version = "dev"
var revision = "unknown"

// env 读取非空环境变量，否则返回默认值；空字符串在这里与未设置采用相同行为。
func env(key, def string) string {
	// if 前的 := 同时声明并推断类型；value 的作用域仅限这个 if（含 else），不像外部变量。
	if value := os.Getenv(key); value != "" {
		return value
	}
	return def
}

// main 程序入口只处理最终错误；实际流程放进可返回 error 的 run。
// log.Fatal 输出日志后调用 os.Exit(1)，不会执行当前 goroutine 尚未运行的 defer；
// run 内的清理会先随 run 返回而执行，再由这里结束进程。
func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

// run 解析命令并组装数据库、HTTP 服务和后台任务；args 不含程序名。
// Go 常用 (结果, error) 返回失败，调用者显式判断，不依赖 Java 式异常向上抛出。
// serve 分支负责资源生命周期：收到退出信号后取消任务，等待后台退出，再释放数据库。
func run(args []string) error {
	startedAt := time.Now().UnixMilli()
	command := "serve"
	if len(args) > 0 {
		// 多重赋值先计算右侧再赋给左侧；args[1:] 是共享底层数组的 slice 视图，并非复制数组。
		command, args = args[0], args[1:]
	}
	if command == "version" {
		fmt.Printf("%s %s\n", version, revision)
		return nil
	}
	if handled, err := backupCommand(command, args); handled {
		return err
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	// flags.String/Bool 返回指向解析结果的指针，Parse 后用 *path、*linuxdoCleanup 读取值。
	// 这不是 Java 装箱类型；& 取地址，* 解引用，Go 不支持指针算术。
	path := flags.String("db", filepath.Join(env("DATA_DIR", "data"), tracking.DatabaseName), "existing SQLite database path")
	listen := flags.String("listen", env("LISTEN_ADDR", ":8080"), "HTTP listen address")
	static := flags.String("static", env("STATIC_DIR", "dist"), "built frontend directory")
	report := flags.String("report", "", "private JSON report path")
	linuxdoCleanup := flags.Bool("linuxdo-cleanup", false, "serve queries and backups while cleaning Linux.do; keep collection paused")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	if *linuxdoCleanup && command != "serve" {
		return errors.New("--linuxdo-cleanup is only valid with serve")
	}
	switch command {
	case "migrate":
		return tracking.Migrate(*path)
	case "init":
		return tracking.Initialize(*path)
	case "verify":
		store, err := tracking.Open(*path)
		if err != nil {
			return err
		}
		// defer 登记在当前函数返回时执行；同一函数的多个 defer 后注册先执行（LIFO）。
		// 它不是整个块结束就执行；这里 verify 分支返回时才关闭连接池并释放目录锁。
		defer store.Close()
		result, err := tracking.Audit(store.DB)
		if err != nil {
			return err
		}
		if *report != "" {
			raw, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				return err
			}
			return os.WriteFile(*report, append(raw, '\n'), 0600)
		}
		fmt.Printf("database verified: projects=%d snapshots=%d integrity=%s foreignKeys=%s\n", result.Tables["projects"].Rows, result.SnapshotPoints, result.Integrity, result.ForeignKeys)
		return nil
	case "serve":
		if _, err := os.Stat(filepath.Join(*static, "index.html")); err != nil {
			return fmt.Errorf("built frontend is unavailable: %w", err)
		}
		store, err := tracking.Open(*path)
		if err != nil {
			return err
		}
		defer store.Close()
		// context.Background 是根上下文；SIGINT/SIGTERM 会取消这个派生 context。
		// context 通过协作通知退出，不会强制杀死 goroutine；每个任务都要观察 Done 或 Err。
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		token, err := runtimeSecret("GITHUB_TOKEN")
		if err != nil {
			return err
		}
		stopBackups, err := startBackupLoop(ctx, store)
		if err != nil {
			return err
		}
		defer stopBackups()
		proxies, err := tracking.ParseProxyNetworks(os.Getenv("TRUSTED_PROXY_CIDRS"))
		if err != nil {
			return fmt.Errorf("invalid TRUSTED_PROXY_CIDRS: %w", err)
		}
		scheduler := tracking.NewScheduler(store, tracking.SchedulerOptions{Token: token, StartedAt: startedAt, LinuxdoCleanup: *linuxdoCleanup, BackupDir: env("BACKUP_DIR", "")})
		submissions := tracking.NewSubmissionWorker(scheduler)
		// 无缓冲 channel 在这里充当完成通知，不传业务数据；close 后接收方可以继续执行。
		// go func() {...}() 启动 goroutine：由 Go 运行时调度的并发任务，并非“一任务固定一个 OS 线程”。
		submitted := make(chan struct{})
		go func() { defer close(submitted); submissions.Run(ctx) }()
		// 这里 defer 的是闭包，退出时先请求取消，再接收完成通知以等待任务结束。
		// 后面登记的 scheduler 清理先执行，随后投稿、备份清理，最后才轮到较早登记的 store.Close。
		defer func() { stop(); <-submitted }()
		scheduled := make(chan struct{})
		go func() { defer close(scheduled); scheduler.Run(ctx) }()
		defer func() { stop(); <-scheduled }()
		if *linuxdoCleanup {
			log.Print("Linux.do cleanup maintenance mode: collection and retention remain paused")
		} else {
			log.Printf("collection requires Linux.do cleanup clearance and waits until %s", time.UnixMilli(tracking.NextSlot(startedAt)).UTC().Format(time.RFC3339))
		}
		// &T{...} 创建结构体值并取指针；字段名初始化比按位置填写更直观。
		// Duration 以 time.Second 等单位构造；1 << 20 是左移，表示 1 MiB 的请求头上限。
		server := &http.Server{Addr: *listen, Handler: tracking.Handler(store, tracking.HTTPOptions{AllowedOrigin: os.Getenv("ALLOWED_ORIGIN"), StaticDir: *static, Submissions: submissions, TrustedProxies: proxies}), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
		go func() {
			<-ctx.Done()
			// 父 ctx 已取消，关闭服务器需另用未取消的根上下文创建十秒超时，给在途请求收尾机会。
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		}()
		log.Printf("trending %s (%s) listening on %s", version, revision, *listen)
		// 正常 Shutdown 会使 ListenAndServe 返回 ErrServerClosed，用 errors.Is 将这种预期退出排除。
		// = 复用外层 err，而 := 声明新变量；两者的作用域和遮蔽行为不同。
		if err = server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q (use serve, init, migrate, verify, backup, restore, version)", command)
	}
}

// runtimeSecret 读取运行时秘密：先取环境变量，再尝试 NAME_FILE 指向的文件，均去除首尾空白。
// 错误只包含变量名，不暴露秘密值；秘密不会传入前端构建或日志。
func runtimeSecret(name string) (string, error) {
	if value := os.Getenv(name); value != "" {
		return strings.TrimSpace(value), nil
	}
	if path := os.Getenv(name + "_FILE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("cannot read runtime secret %s", name)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	return "", nil
}
