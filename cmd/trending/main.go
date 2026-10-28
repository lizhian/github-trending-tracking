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
	command := "serve"
	if len(args) > 0 {
		command, args = args[0], args[1:]
	}
	if command == "version" {
		fmt.Printf("%s %s\n", version, revision)
		return nil
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	path := flags.String("db", filepath.Join(env("DATA_DIR", "data"), tracking.DatabaseName), "existing SQLite database path")
	listen := flags.String("listen", env("LISTEN_ADDR", ":8080"), "HTTP listen address")
	static := flags.String("static", env("STATIC_DIR", "dist"), "built frontend directory")
	source := flags.String("source", "", "frozen Wrangler D1 SQL export")
	report := flags.String("report", "", "private JSON report path")
	stopped := flags.Bool("old-writer-stopped", false, "acknowledge old scheduler and in-flight writes have stopped (allows clearing legacy lease)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	switch command {
	case "init":
		return tracking.Initialize(*path)
	case "import":
		result, err := tracking.ImportD1(*source, *path, *report, *stopped)
		if err != nil {
			return err
		}
		fmt.Printf("import validated: projects=%d snapshots=%d leaseCleared=%t; private report saved\n", result.Target.Tables["projects"].Rows, result.Target.SnapshotPoints, result.LeaseCleared)
		return nil
	case "verify":
		store, err := tracking.Open(*path)
		if err != nil {
			return err
		}
		defer store.Close()
		result, err := tracking.Audit(store.DB, false)
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
		server := &http.Server{Addr: *listen, Handler: tracking.Handler(store, tracking.HTTPOptions{AllowedOrigin: os.Getenv("ALLOWED_ORIGIN"), StaticDir: *static}), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
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
		return fmt.Errorf("unknown command %q (use serve, init, import, verify, version)", command)
	}
}
