package tracking_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lizhian/github-trending-tracking/internal/tracking"
)

func backupFixture(t *testing.T) (*tracking.Store, string, time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), tracking.DatabaseName)
	if err := tracking.Initialize(path); err != nil {
		t.Fatal(err)
	}
	store, err := tracking.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	now := time.Date(2026, 10, 1, 5, 4, 0, 0, time.UTC)
	start := now.Add(-2 * time.Hour).UnixMilli()
	end := now.UnixMilli()
	trends := map[int]tracking.TrendSummary{}
	for _, width := range tracking.Windows {
		delta := int64(10)
		a, b := start, end
		trends[width] = tracking.TrendSummary{Delta: &delta, State: "partial", Start: &a, End: &b, Gaps: []tracking.Gap{}}
	}
	raw, _ := json.Marshal(trends)
	_, err = store.DB.Exec("INSERT INTO projects(id,cycle,name,topics,entered_at,listed_at,observed_at,attempted_at,stars,trends,type,repo_id) VALUES('github:R_1','original','acme/one','[\"tools\"]',?,?,?,?,110,?,'github','R_1')", start-tracking.Day, start, end, end, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	points, _ := json.Marshal(map[string]tracking.Point{fmtInt(start / tracking.Hour): {start, 100}, fmtInt(end / tracking.Hour): {end, 110}})
	if _, err = store.DB.Exec("INSERT INTO snapshot_days VALUES('original',?,?)", end/tracking.Day, string(points)); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{"sampling_policy": tracking.SamplingPolicy{Version: 2, EffectiveAt: start - tracking.Day}, "pending": []any{map[string]any{"name": "later/repo", "nextAt": end + tracking.Hour}}, "upstream": map[string]any{"api.github.com": map[string]any{"blockedUntil": end + 2*tracking.Hour}}} {
		if err = store.SetState(key, value); err != nil {
			t.Fatal(err)
		}
	}
	return store, path, now
}
func fmtInt(n int64) string { raw, _ := json.Marshal(n); return string(raw) }
func TestConsistentBackupRestoresHistoryAndStateWithoutReplacingExistingFiles(t *testing.T) {
	store, path, now := backupFixture(t)
	directory := t.TempDir()
	before, err := tracking.Audit(store.DB)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Backup(directory, now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, result.Audit) {
		t.Fatal("backup changed physical data or business state")
	}
	store.Close() // actual stopped application before offline restore
	restored := filepath.Join(t.TempDir(), tracking.DatabaseName)
	if err = tracking.Restore(result.Path, restored); err != nil {
		t.Fatal(err)
	}
	if err = tracking.Restore(result.Path, path); err == nil {
		t.Fatal("restore replaced existing database")
	}
	recovered, err := tracking.Open(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	after, err := tracking.Audit(recovered.DB)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("restore lost history or state")
	}
	handler := tracking.Handler(recovered, tracking.HTTPOptions{Now: func() time.Time { return now }})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/projects/github%3AR_1/history", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var history tracking.HistoryResult
	if err = json.Unmarshal(w.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Points) != 2 || history.Trend.Delta == nil || *history.Trend.Delta != 10 {
		t.Fatal("restored history cannot be queried", w.Body)
	}
	original, err := os.ReadFile(path)
	if err != nil || len(original) == 0 {
		t.Fatal("original database was not preserved")
	}
}

func TestDailyBackupRetainsThreeDaysAndFailureKeepsExistingCopies(t *testing.T) {
	store, _, now := backupFixture(t)
	directory := t.TempDir()
	var paths []string
	for day := 0; day < 4; day++ {
		result, err := store.Backup(directory, now.Add(time.Duration(day)*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, result.Path)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("retained %d snapshots, want3", len(entries))
	}
	if _, err = os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Fatal("backup older than retention survived")
	}
	// A real database failure must happen before any retention pruning.
	store.Close()
	if _, err = store.Backup(directory, now.Add(5*24*time.Hour)); err == nil {
		t.Fatal("backup succeeded on closed database")
	}
	after, err := os.ReadDir(directory)
	if err != nil || len(after) != 3 {
		t.Fatal("failed backup deleted prior copies")
	}
	for _, entry := range after {
		target := filepath.Join(t.TempDir(), tracking.DatabaseName)
		if err = tracking.Restore(filepath.Join(directory, entry.Name()), target); err != nil {
			t.Fatal("prior backup became unusable", err)
		}
	}
}
func TestDailyBackupRunsDuringCollectionStartupWaitAndSurvivesRestart(t *testing.T) {
	store, _, now := backupFixture(t)
	directory := t.TempDir()
	// 13:04 Beijing, before the next collection round at15:00. Daily maintenance
	// must still run; the runner does not depend on a collection-gate timestamp.
	runner, err := tracking.NewBackupRunner(store, directory, "03:00")
	if err != nil {
		t.Fatal(err)
	}
	if result, err := runner.Tick(now); err != nil || result == nil {
		t.Fatal("daily backup was blocked", err)
	}
	if result, err := runner.Tick(now.Add(time.Minute)); err != nil || result != nil {
		t.Fatal("backed up twice in same daily slot", err)
	}
	restarted, err := tracking.NewBackupRunner(store, directory, "03:00")
	if err != nil {
		t.Fatal(err)
	}
	if result, err := restarted.Tick(now.Add(2 * time.Minute)); err != nil || result != nil {
		t.Fatal("restart duplicated validated daily snapshot", err)
	}
	if result, err := restarted.Tick(now.Add(24 * time.Hour)); err != nil || result == nil {
		t.Fatal("next daily backup did not run", err)
	}
	if _, err = tracking.NewBackupRunner(store, directory, "3am"); err == nil {
		t.Fatal("invalid deployment time accepted")
	}
}

func TestBackupIsConsistentWhileWALTransactionsCommit(t *testing.T) {
	store, path, now := backupFixture(t)
	directory := t.TempDir()
	writer, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errors := make(chan error, 1)
	ready := make(chan struct{})
	var commits atomic.Int64
	go func() {
		first := true
		for ctx.Err() == nil {
			tx, err := writer.BeginTx(ctx, nil)
			if err != nil {
				if ctx.Err() == nil {
					errors <- err
				}
				break
			}
			n := commits.Load() + 1
			_, err = tx.Exec("INSERT INTO system_state(key,value) VALUES('collection',?),('collection:github',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", fmtInt(n), fmtInt(n))
			if err == nil {
				err = tx.Commit()
			} else {
				tx.Rollback()
			}
			if err != nil {
				if ctx.Err() == nil {
					errors <- err
				}
				break
			}
			commits.Add(1)
			if first {
				close(ready)
				first = false
			}
		}
		close(errors)
	}()
	<-ready
	result, err := store.Backup(directory, now)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if commits.Load() < 2 {
		t.Fatal("WAL writer did not continue while snapshot was created")
	}
	target := filepath.Join(t.TempDir(), tracking.DatabaseName)
	if err = tracking.Restore(result.Path, target); err != nil {
		t.Fatal(err)
	}
	restored, err := tracking.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var global, typed int64
	if _, err = restored.State("collection", &global); err != nil {
		t.Fatal(err)
	}
	if _, err = restored.State("collection:github", &typed); err != nil {
		t.Fatal(err)
	}
	if global == 0 || global != typed {
		t.Fatalf("torn transaction in restored snapshot: %d vs %d", global, typed)
	}
}
