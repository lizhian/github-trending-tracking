package tracking

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

func TestSubmissionConcurrentDedupCapacityAndExpiry(t *testing.T) {
	store, _ := submissionStore(t)
	ctx := context.Background()
	now := int64(20 * Day)
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

type testSubmissionControl struct{ paused bool }

func (c *testSubmissionControl) Paused(context.Context) (bool, error) { return c.paused, nil }
func (c *testSubmissionControl) Wake()                                {}

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
	// An untrusted peer cannot rotate its quota by spoofing forwarding headers.
	for i := 0; i < 5; i++ {
		if w := request("POST", "/api/submissions", `{"url":"https://github.com/a/b"}`, "203.0.113.5:12", fmt.Sprintf("192.0.2.%d", i)); w.Code != 202 {
			t.Fatal(w.Code, w.Body)
		}
	}
	if w := request("POST", "/api/submissions", `{"url":"https://github.com/a/b"}`, "203.0.113.5:12", "192.0.2.200"); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal(w.Code, w.Body)
	}
	// Distinct real clients behind a trusted proxy retain independent quotas.
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

func TestMigrationPreservesExistingDataAndBackup(t *testing.T) {
	for _, version := range []int{3, 4, 5} {
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
			// Older snapshots must remain verifiable and restorable before upgrading.
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
