package tracking

// 本文件使用 package tracking 同包测试，可以调用小写内部函数，适合验证地址规则与迁移边界。
// 这与 scheduler_test.go 的外部包测试互补；使用真实临时 SQLite，不操作开发/生产数据库。

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lizhian/github-trending-tracking/migrations"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// submissionStore 建立本包测试使用的真实临时 SQLite，返回库和路径，并登记清理。
func submissionStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), DatabaseName)
	if err := Initialize(path); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store, path
}

// TestSubmissionAddressRules 逐组检查合法地址规范化与非法地址拒绝，尤其排除附件、子页面、伪造主机和编码路径。
func TestSubmissionAddressRules(t *testing.T) {
	for _, input := range []string{"https://github.com/user-attachments/assets", "https://github.com/USER-ATTACHMENTS/assets.git/"} {
		if name, err := submissionName(input); err == nil {
			t.Errorf("accepted attachment %q as %q", input, name)
		}
	}
	for _, input := range []string{"https://github.com/Acme/Tool", "https://GITHUB.com/Acme/Tool.git/", " https://github.com/Acme/Tool/?tab=readme "} {
		got, err := submissionName(input)
		if err != nil || got != "acme/tool" {
			t.Errorf("%q: %q %v", input, got, err)
		}
	}
	for _, input := range []string{"http://github.com/a/b", "https://gitlab.com/a/b", "https://github.com/a/b/tree/main", "https://user@github.com/a/b", "https://github.com:444/a/b", "https://github.com/topics/go", "https://github.com/a/%62", "https://github.com/a/..", "https://github.com/a/b//", "https://github.com/a"} {
		if name, err := submissionName(input); err == nil {
			t.Errorf("accepted %q as %q", input, name)
		}
	}
}

// TestSubmissionConcurrentDedupCapacityAndExpiry 并发提交同一仓库验证合并，再检查满队列仍可合并、七天结果边界和完成后重新投稿产生新任务。
func TestSubmissionConcurrentDedupCapacityAndExpiry(t *testing.T) {
	store, _ := submissionStore(t)
	ctx := context.Background()
	now := int64(20 * Day)
	// WaitGroup 等待二十个 goroutine 全部结束；Add 必须先于启动，defer Done 保证每个任务归还计数。
	// 工作 goroutine 用 t.Error 报错并 return；t.Fatal 会终止当前 goroutine，不应用它代替主测试退出。
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sub, err := store.EnqueueSubmission(ctx, "https://github.com/Acme/Tool.git", now)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- sub.ID
		}()
	}
	// 所有发送者结束后由主测试关闭 ids；range ids 收完缓冲结果后退出，不与发送发生关闭竞态。
	wg.Wait()
	close(ids)
	id := ""
	for got := range ids {
		if id != "" && id != got {
			t.Fatal("concurrent duplicate created a second task")
		}
		id = got
	}
	for i := 0; i < 99; i++ {
		if _, err := store.EnqueueSubmission(ctx, fmt.Sprintf("https://github.com/a/r%d", i), now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.EnqueueSubmission(ctx, "https://github.com/a/overflow", now); err != ErrSubmissionCapacity {
		t.Fatal(err)
	}
	if sub, err := store.EnqueueSubmission(ctx, "https://github.com/ACME/TOOL", now); err != nil || sub.ID != id {
		t.Fatal("full queue did not merge duplicate", err)
	}
	if _, err := store.DB.Exec("UPDATE submissions SET phase='done',github='tracked',linuxdo='ineligible',finished_at=? WHERE id=?", now, id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Submission(id, now+7*Day-1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Submission(id, now+7*Day); err == nil {
		t.Fatal("expired result remained queryable")
	}
	newer, err := store.EnqueueSubmission(ctx, "https://github.com/acme/tool", now+Day)
	if err != nil || newer.ID == id {
		t.Fatal("ended task was reused", err)
	}
}

// testSubmissionControl 只控制是否暂停的接口替身，让 HTTP 测试不必启动真实后台消费者。
type testSubmissionControl struct{ paused bool }

// Paused 返回测试设置的暂停标志；不执行网络或维护流程。
func (c *testSubmissionControl) Paused(context.Context) (bool, error) { return c.paused, nil }

// Wake 空实现满足 SubmissionControl 接口；本组测试只检查 HTTP 接收与持久入队。
func (c *testSubmissionControl) Wake()                                {}

// TestSubmissionHTTPBoundariesAndProxyLimits 验证 202 接收、状态查询、可信代理/IP 限额、维护门禁、JSON 边界、预检与来源校验。
func TestSubmissionHTTPBoundariesAndProxyLimits(t *testing.T) {
	store, _ := submissionStore(t)
	control := &testSubmissionControl{}
	now := time.UnixMilli(20 * Day)
	proxies, _ := ParseProxyNetworks("10.0.0.0/8")
	h := Handler(store, HTTPOptions{AllowedOrigin: "https://site.test", Now: func() time.Time { return now }, Submissions: control, TrustedProxies: proxies})
	request := func(method, path, body, remote, forwarded string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = remote
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://site.test")
		r.Header.Set("X-Forwarded-For", forwarded)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := request("POST", "/api/submissions", `{"url":"https://github.com/a/b"}`, "10.0.0.1:1234", "192.0.2.1")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body)
	}
	var sub Submission
	if err := json.Unmarshal(w.Body.Bytes(), &sub); err != nil {
		t.Fatal(err)
	}
	if sub.URL != "https://github.com/a/b" || sub.Status != "queued" {
		t.Fatal(sub)
	}
	if w := request("GET", "/api/submissions/"+sub.ID, "", "203.0.113.1:1234", ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	// 不可信连接对端不能靠伪造转发头轮换 IP 配额。
	for i := 0; i < 5; i++ {
		if w := request("POST", "/api/submissions", `{"url":"https://github.com/a/b"}`, "203.0.113.5:12", fmt.Sprintf("192.0.2.%d", i)); w.Code != 202 {
			t.Fatal(w.Code, w.Body)
		}
	}
	if w := request("POST", "/api/submissions", `{"url":"https://github.com/a/b"}`, "203.0.113.5:12", "192.0.2.200"); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal(w.Code, w.Body)
	}
	// 可信代理后的不同真实客户端应拥有独立配额。
	if w := request("POST", "/api/submissions", `{"url":"https://github.com/a/b"}`, "10.0.0.1:12", "192.0.2.2"); w.Code != 202 {
		t.Fatal(w.Code, w.Body)
	}
	now = now.Add(time.Minute)
	if w := request("POST", "/api/submissions", `{"url":"https://github.com/a/b"}`, "203.0.113.5:12", ""); w.Code != 202 {
		t.Fatal(w.Code, w.Body)
	}
	control.paused = true
	if w := request("POST", "/api/submissions", `{"url":"https://github.com/a/b"}`, "203.0.113.6:12", ""); w.Code != 503 {
		t.Fatal(w.Code)
	}
	if w := request("GET", "/api/submissions/"+sub.ID, "", "203.0.113.6:12", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	control.paused = false
	if w := request("POST", "/api/submissions", `{"url":"https://github.com/a/b","extra":1}`, "203.0.113.7:12", ""); w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
	if w := request("POST", "/api/submissions", `{"url":"https://github.com/a/b"}`+strings.Repeat(" ", 4096), "203.0.113.8:12", ""); w.Code != 413 {
		t.Fatal(w.Code, w.Body)
	}
	if w := request("OPTIONS", "/api/submissions", "", "203.0.113.9:12", ""); w.Code != 204 || !strings.Contains(w.Header().Get("Access-Control-Allow-Methods"), "POST") {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest("POST", "/api/submissions", strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://other.test")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal(w.Code)
	}
}

// TestMigrationPreservesExistingDataAndBackup 对 schema 3/4/5 分别准备旧库，升级后验证数据/重试保留、旧字段移除、重复升级安全和投稿队列可备份恢复。
func TestMigrationPreservesExistingDataAndBackup(t *testing.T) {
	for _, version := range []int{3, 4, 5} {
		// 每个版本是独立子测试，有自己的 *testing.T 与临时目录；没有 t.Parallel，因此顺序执行。
		t.Run(fmt.Sprintf("schema-%d", version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), DatabaseName)
			db, err := database(path, "rwc")
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range MigrationNames[:version] {
				source, _ := migrations.Files.ReadFile(name)
				if _, err = db.Exec(string(source)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version)); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO projects(id,cycle,name,topics,entered_at,listed_at,observed_at,attempted_at,stars,trends,type,repo_id) VALUES('github:R1','old-cycle','a/b','[]',1,2,3,3,10,'{}','github','R1'); INSERT INTO snapshot_days VALUES('old-cycle',0,'{"0":[3,10]}'); INSERT INTO system_state VALUES('pending','[{"name":"a/c","listedAt":2,"nextAt":99,"readmeNextAt":100,"failure":"retry"}]'); INSERT INTO system_state VALUES('discoverySources','{"details":[{"url":"https://hellogithub.com/repository/one","listedAt":2,"nextAt":88,"error":"retry"}],"sources":{"github-daily":{"nextAt":200}}}')`); err != nil {
				t.Fatal(err)
			}
			if version == 5 {
				if _, err = db.Exec("UPDATE projects SET homepage='https://example.com/project'"); err != nil {
					t.Fatal(err)
				}
			}
			preservedColumns := strings.Replace(strings.Join(columnsForVersion("projects", version), ","), ",listed_at", "", 1)
			legacyProjectQuery := "SELECT json_array(" + preservedColumns + ") FROM projects"
			var beforeProject string
			if err = db.QueryRow(legacyProjectQuery).Scan(&beforeProject); err != nil {
				t.Fatal(err)
			}
			before, err := Audit(db)
			if err != nil {
				t.Fatal(err)
			}
			db.Close()
			// 执行升级前，先断言旧快照仍可校验和恢复；恢复兼容性不等同于可直接启动旧 schema。
			oldTarget := filepath.Join(t.TempDir(), DatabaseName)
			if err = Restore(path, oldTarget); err != nil {
				t.Fatal("old backup was rejected", err)
			}
			if err = Migrate(path); err != nil {
				t.Fatal(err)
			}
			if err = Migrate(path); err != nil {
				t.Fatal("migration was not idempotent", err)
			}
			store, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			after, err := Audit(store.DB)
			if err != nil {
				t.Fatal(err)
			}
			for table, digest := range before.Tables {
				if table == "projects" || table == "system_state" {
					continue
				}
				if !reflect.DeepEqual(digest, after.Tables[table]) {
					t.Fatalf("migration changed %s", table)
				}
			}
			var pending []map[string]any
			if found, err := store.State("pending", &pending); err != nil || !found || len(pending) != 1 || pending[0]["listedAt"] != nil || pending[0]["nextAt"] != float64(99) || pending[0]["readmeNextAt"] != float64(100) || pending[0]["failure"] != "retry" {
				t.Fatal("migration changed pending retry state", pending, err)
			}
			var discovery map[string]any
			if found, err := store.State("discoverySources", &discovery); err != nil || !found {
				t.Fatal("migration lost discovery state", err)
			}
			detail := discovery["details"].([]any)[0].(map[string]any)
			if detail["listedAt"] != nil || detail["nextAt"] != float64(88) || detail["error"] != "retry" || discovery["sources"].(map[string]any)["github-daily"].(map[string]any)["nextAt"] != float64(200) {
				t.Fatal("migration changed discovery retry state", discovery)
			}
			var afterProject string
			if err = store.DB.QueryRow(legacyProjectQuery).Scan(&afterProject); err != nil || afterProject != beforeProject {
				t.Fatal("migration changed existing project fields", beforeProject, afterProject, err)
			}
			rows, err := store.ProjectRows("")
			if err != nil || len(rows) != 1 || (version < 5 && rows[0].Homepage != nil) || before.Tables["projects"].Rows != after.Tables["projects"].Rows {
				t.Fatal("migration lost projects or supplied a homepage", rows, err)
			}
			if err = Migrate(path); err == nil {
				t.Fatal("migration allowed a live writer")
			}
			sub, err := store.EnqueueSubmission(context.Background(), "https://github.com/a/c", 20*Day)
			if err != nil {
				t.Fatal(err)
			}
			backup, err := store.Backup(t.TempDir(), time.UnixMilli(20*Day))
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), DatabaseName)
			if err = Restore(backup.Path, target); err != nil {
				t.Fatal(err)
			}
			restored, err := Open(target)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			if got, err := restored.Submission(sub.ID, 20*Day); err != nil || got.Name != sub.Name {
				t.Fatal("queue missing after restore", err)
			}
		})
	}
}

// TestSubmissionMigrationFailureRollsBackDDLAndVersion 先制造迁移索引名称冲突，再验证失败后版本仍为 3 且新表不存在，证明 DDL 和版本同事务回滚。
func TestSubmissionMigrationFailureRollsBackDDLAndVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), DatabaseName)
	db, err := database(path, "rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range MigrationNames[:3] {
		source, _ := migrations.Files.ReadFile(name)
		if _, err = db.Exec(string(source)); err != nil {
			t.Fatal(err)
		}
	}
	// 准备故障：让旧库预先占用后续迁移的索引名称，真实触发 DDL 失败，而非 mock 一个返回值。
	if _, err = db.Exec("PRAGMA user_version=3; CREATE INDEX submissions_active_name ON projects(name)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err = Migrate(path); err == nil {
		t.Fatal("conflicting DDL unexpectedly succeeded")
	}
	db, err = database(path, "rw")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, count int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='submissions'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if version != 3 || count != 0 {
		t.Fatalf("failed migration left version=%d submissions=%d", version, count)
	}
}
