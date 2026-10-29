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

var version = "dev"
var revision = "unknown"

func env(key, def string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return def
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}
func run(args []string) error {
	startedAt := time.Now().UnixMilli()
	command := "serve"
	if len(args) > 0 {
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
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		token, err := runtimeSecret("GITHUB_TOKEN")
		if err != nil {
			return err
		}
		fallback, err := runtimeSecret("LINUXDO_RSS_FALLBACK_URL")
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
		scheduler := tracking.NewScheduler(store, tracking.SchedulerOptions{Token: token, FallbackURL: fallback, StartedAt: startedAt, LinuxdoCleanup: *linuxdoCleanup, BackupDir: env("BACKUP_DIR", "")})
		submissions := tracking.NewSubmissionWorker(scheduler)
		submitted := make(chan struct{})
		go func() { defer close(submitted); submissions.Run(ctx) }()
		defer func() { stop(); <-submitted }()
		scheduled := make(chan struct{})
		go func() { defer close(scheduled); scheduler.Run(ctx) }()
		defer func() { stop(); <-scheduled }()
		if *linuxdoCleanup {
			log.Print("Linux.do cleanup maintenance mode: collection and retention remain paused")
		} else {
			log.Printf("collection requires Linux.do cleanup clearance and waits until %s", time.UnixMilli(tracking.NextSlot(startedAt)).UTC().Format(time.RFC3339))
		}
		server := &http.Server{Addr: *listen, Handler: tracking.Handler(store, tracking.HTTPOptions{AllowedOrigin: os.Getenv("ALLOWED_ORIGIN"), StaticDir: *static, Submissions: submissions, TrustedProxies: proxies}), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		}()
		log.Printf("trending %s (%s) listening on %s", version, revision, *listen)
		if err = server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q (use serve, init, migrate, verify, backup, restore, version)", command)
	}
}

// Runtime secrets are never passed to frontend builds or included in logs.
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
