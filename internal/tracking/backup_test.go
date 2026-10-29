package tracking_test

// 本文件通过公开接口验证一致备份、离线恢复与轮转；使用真实临时 SQLite，包括 WAL 并发写入。
// 先读 backupFixture 理解测试数据，再跟随“准备 → 备份/恢复 → 审计与 HTTP 断言”的顺序。

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

// backupFixture 准备含真实历史、采样边界、待重试任务和冷却的 SQLite，使备份比较覆盖业务状态而不仅是文件存在。
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
	_, err = store.DB.Exec("INSERT INTO projects(id,cycle,name,topics,entered_at,observed_at,attempted_at,stars,trends,type,repo_id) VALUES('github:R_1','original','acme/one','[\"tools\"]',?,?,?,110,?,'github','R_1')", start-tracking.Day, end, end, string(raw))
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

// fmtInt 把整数转为 JSON 十进制文本，作为快照桶键或测试状态值。
func fmtInt(n int64) string { raw, _ := json.Marshal(n); return string(raw) }

// TestConsistentBackupRestoresHistoryAndStateWithoutReplacingExistingFiles 备份后停库并恢复到新路径，
// 比对完整审计和历史 API；同时验证不覆盖现有库且原文件保留。
func TestConsistentBackupRestoresHistoryAndStateWithoutReplacingExistingFiles(t *testing.T) {
	// 准备：写入历史和持久化状态，先做完整审计作为恢复比较基准。
	store, path, now := backupFixture(t)
	directory := t.TempDir()
	before, err := tracking.Audit(store.DB)
	if err != nil {
		t.Fatal(err)
	}
	// 执行：从正在打开的库生成一致快照；后面明确关闭原库，再执行离线恢复。
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
	// 断言：报告包含全部物理历史与调度检查点，不能只比较项目数或文件大小。
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

// TestDailyBackupRetainsThreeDaysAndFailureKeepsExistingCopies 连续创建四天快照，确认只保留三天；再关闭库制造失败，验证已有副本不被删且仍可恢复。
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
	// 先关闭库制造真实失败，确认轮转删除必须发生在新备份成功之后。
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

// TestDailyBackupRunsDuringCollectionStartupWaitAndSurvivesRestart 验证备份不依赖采集启动门禁、同一每日时段不重复、重建 runner 后仍能识别已有成功快照。
func TestDailyBackupRunsDuringCollectionStartupWaitAndSurvivesRestart(t *testing.T) {
	store, _, now := backupFixture(t)
	directory := t.TempDir()
	// 此时是北京时间 13:04；这里只验证 runner 不依赖采集门禁，不构造具体采集轮次。
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

// TestBackupIsConsistentWhileWALTransactionsCommit 让另一连接持续在事务里更新两个关联状态，同时创建备份；恢复后两值必须相等，不能读到半个事务。
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
	// 缓冲一个错误，使写 goroutine 报错时不必立刻等主测试接收；ready 用于确认至少已有一次提交。
	// 变量 errors 在这里是 channel 名，与标准库 errors 包没有关联。
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
	// 等待首笔事务确实完成再备份，比用固定 sleep 猜测并发时序可靠。
	<-ready
	result, err := store.Backup(directory, now)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	// range channel 持续接收直到通道关闭；此处同时等待写 goroutine 收尾，并收集其失败。
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
