package tracking_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lizhian/github-trending-tracking/internal/tracking"
)

const linuxRSS = "https://linux.do/tag/2234-tag/2234.rss"
const readmeURL = "https://api.github.com/repos/acme/one/readme"
const cleanupKey = "linuxdo_cleanup_v1"

func linuxFeed(names ...string) func() *http.Response {
	return func() *http.Response {
		text := "<rss><channel><item><description><![CDATA["
		for _, name := range names {
			text += " https://github.com/" + name
		}
		return response(text + "]]></description></item></channel></rss>")
	}
}
func setPending(t *testing.T, h *collectionHarness, values ...map[string]any) {
	t.Helper()
	if err := h.store.SetState("pending", values); err != nil {
		t.Fatal(err)
	}
}
func cacheExpiry(t *testing.T, h *collectionHarness, id string) int64 {
	t.Helper()
	var result struct {
		ExpiresAt int64 `json:"expiresAt"`
	}
	found, err := h.store.State("linuxdo_readme:"+id, &result)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		return 0
	}
	return result.ExpiresAt
}
func TestLinuxdoReadmeLiteralQualification(t *testing.T) {
	for _, test := range []struct {
		name, text string
		eligible   bool
	}{
		{"link", "[community](https://linux.do/t/123)", true},
		{"code-and-comment", "<!-- https://linux.do -->\n```https://linux.do```", true},
		{"literal-prefix", "https://linux.docker.example", true},
		{"case-sensitive", "https://LINUX.DO", false},
		{"missing-marker", "See http://linux.do and https://example.com", false},
		{"empty", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
			h.sources[linuxRSS] = linuxFeed("acme/one")
			h.sources[readmeURL] = func() *http.Response { return response(test.text) }
			h.tick(beijing("2026-10-01T09:00:00"))
			want := float64(0)
			if test.eligible {
				want = 1
			}
			if got := h.list("linux.do")["total"]; got != want {
				t.Fatal(got, "want", want)
			}
			if got := h.list("github")["total"]; got != float64(2) {
				t.Fatal("GitHub qualification changed", got)
			}
			if (cacheExpiry(t, h, "R_0") > 0) == test.eligible {
				t.Fatal("wrong negative cache")
			}
		})
	}
}

func TestLinuxdoNegativeCacheSurvivesRestartRenameAndRequiresRediscovery(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	requests := 0
	h.sources[readmeURL] = func() *http.Response { requests++; return response("unrelated dependency") }
	setPending(t, h, map[string]any{"type": "linux.do", "repoId": "R_0", "name": "acme/one", "listedAt": h.clock.at})
	h.tick(beijing("2026-10-01T09:00:00"))
	expires := cacheExpiry(t, h, "R_0")
	var checked struct {
		CheckedAt int64 `json:"checkedAt"`
	}
	if _, err := h.store.State("linuxdo_readme:R_0", &checked); err != nil {
		t.Fatal(err)
	}
	if expires != checked.CheckedAt+tracking.Day || checked.CheckedAt > h.clock.at || requests != 1 {
		t.Fatal("negative TTL must start at completed check", expires, h.clock.at, requests)
	}
	h.names[0] = "acme/renamed"
	h.sources["https://api.github.com/repos/acme/renamed/readme"] = func() *http.Response { requests++; return response("https://linux.do") }
	h.scheduler = tracking.NewScheduler(h.store, tracking.SchedulerOptions{Clock: h.clock, Client: h.client, StartedAt: h.clock.at})
	setPending(t, h, map[string]any{"type": "linux.do", "repoId": "R_0", "name": "acme/renamed", "listedAt": h.clock.at})
	h.tick(beijing("2026-10-01T11:00:00"))
	if requests != 1 || h.list("linux.do")["total"] != float64(0) {
		t.Fatal("cache lost on restart or rename")
	}
	h.tick(expires - 1)
	if requests != 1 || cacheExpiry(t, h, "R_0") != expires {
		t.Fatal("cache expired early")
	}
	h.tick(expires)
	if requests != 1 || cacheExpiry(t, h, "R_0") != 0 {
		t.Fatal("expiry must not trigger a request")
	}
	h.sources[linuxRSS] = linuxFeed("acme/renamed")
	h.tick(beijing("2026-10-02T11:00:00"))
	if requests != 2 || h.list("linux.do")["total"] != float64(1) {
		t.Fatal("rediscovery did not requalify")
	}
	first := h.list("linux.do")["items"].([]any)[0].(map[string]any)
	firstCycleRows, err := h.store.ProjectRows("WHERE type='linux.do'")
	if err != nil {
		t.Fatal(err)
	}
	h.tick(beijing("2026-10-02T13:00:00"))
	if requests != 2 {
		t.Fatal("in-library repository was rechecked")
	}
	if _, err := h.store.DB.Exec("DELETE FROM projects WHERE type='linux.do'"); err != nil {
		t.Fatal(err)
	}
	h.tick(beijing("2026-10-02T15:00:00"))
	again, err := h.store.ProjectRows("WHERE type='linux.do'")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 3 || len(again) != 1 || again[0].Cycle == firstCycleRows[0].Cycle || again[0].EnteredAt <= int64(first["enteredAt"].(float64)) {
		t.Fatal("reentry must requalify and create a cycle")
	}
	if points := h.history("linux.do:R_0")["points"].([]any); len(points) != 1 {
		t.Fatal("reentry copied old history", points)
	}
}

func TestLinuxdoReadmeFailuresAndAnonymous404Confirmation(t *testing.T) {
	for _, test := range []struct {
		name               string
		status             int
		metadata           string
		anonymous          int
		text               string
		eligible, rejected bool
		failRead           bool
		rateLimit          bool
	}{
		{name: "missing-readme", status: 404, metadata: `{"node_id":"R_0","full_name":"acme/one","private":false}`, anonymous: 404, rejected: true},
		{name: "token-contents-permission", status: 404, metadata: `{"node_id":"R_0","full_name":"acme/one","private":false}`, anonymous: 200, text: "https://linux.do", eligible: true},
		{name: "reused-name", status: 404, metadata: `{"node_id":"R_other","full_name":"acme/one","private":false}`, anonymous: 404},
		{name: "private-metadata", status: 404, metadata: `{"node_id":"R_0","full_name":"acme/one","private":true}`, anonymous: 404},
		{name: "inaccessible-repository", status: 404, anonymous: 404},
		{name: "server-error", status: 503},
		{name: "unexpected-empty-status", status: 204},
		{name: "unauthorized", status: 401},
		{name: "rate-limit", status: 429, rateLimit: true},
		{name: "read-error", status: 200, failRead: true},
		{name: "oversized", status: 200, text: strings.Repeat("x", 2_000_001) + "https://linux.do"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
			base := h.client.Transport
			var lastReadmeAt int64
			h.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() == readmeURL {
					lastReadmeAt = h.clock.at
					if r.Header.Get("Accept") != "application/vnd.github.raw+json" || r.URL.RawQuery != "" {
						t.Fatal("not a default-branch raw README request")
					}
					result := response(test.text)
					result.StatusCode = test.status
					if r.Header.Get("Authorization") == "" {
						result.StatusCode = test.anonymous
					}
					if test.failRead {
						result.Body = io.NopCloser(failingRead{})
					}
					if test.rateLimit {
						result.Header.Set("Retry-After", "7200")
					}
					return result, nil
				}
				if r.URL.String() == "https://api.github.com/repos/acme/one" {
					lastReadmeAt = h.clock.at
					if r.Header.Get("Authorization") != "" {
						t.Fatal("confirmation must be anonymous")
					}
					result := response(test.metadata)
					if test.metadata == "" {
						result.StatusCode = 404
					}
					return result, nil
				}
				return base.RoundTrip(r)
			})
			h.sources[linuxRSS] = linuxFeed("acme/one")
			h.tick(beijing("2026-10-01T09:00:00"))
			want := float64(0)
			if test.eligible {
				want = 1
			}
			if h.list("linux.do")["total"] != want {
				t.Fatal("wrong admission")
			}
			if (cacheExpiry(t, h, "R_0") > 0) != test.rejected {
				t.Fatal("failed requests must not become negative cache")
			}
			var pending []map[string]any
			if _, err := h.store.State("pending", &pending); err != nil {
				t.Fatal(err)
			}
			if !test.eligible && !test.rejected {
				var retry float64
				for _, p := range pending {
					if p["type"] == "linux.do" {
						retry, _ = p["readmeNextAt"].(float64)
					}
				}
				if retry < float64(lastReadmeAt+30*60_000) {
					t.Fatal("missing retry", pending)
				}
				if test.rateLimit && retry < float64(lastReadmeAt+2*tracking.Hour) {
					t.Fatal("ignored longer cooldown")
				}
			}
			if strings.Contains(mustJSON(h.list("linux.do")), "private-token") {
				t.Fatal("leaked credentials")
			}
		})
	}
}

type failingRead struct{}

func (failingRead) Read([]byte) (int, error) { return 0, errors.New("secret upstream read failure") }

func TestLinuxdoReadmeRetryDoesNotDelayGitHubCandidate(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	// Make this an old Linux.do pending before any shared GitHub candidate exists.
	setPending(t, h, map[string]any{"type": "linux.do", "repoId": "R_0", "name": "acme/one", "listedAt": h.clock.at})
	h.sources["https://rsshub-balancer.virworks.moe/github/trending/daily/any"] = func() *http.Response { return response("<rss><channel/></rss>") }
	requests := 0
	h.sources[readmeURL] = func() *http.Response { requests++; r := response("unavailable"); r.StatusCode = 503; return r }
	h.tick(beijing("2026-10-01T09:00:00"))
	var pending []map[string]any
	if _, err := h.store.State("pending", &pending); err != nil {
		t.Fatal(err)
	}
	pending = append(pending, map[string]any{"type": "github", "repoId": "R_0", "name": "acme/one", "listedAt": h.clock.at})
	if err := h.store.SetState("pending", pending); err != nil {
		t.Fatal(err)
	}
	h.tick(beijing("2026-10-01T09:01:00"))
	if requests != 1 || h.list("github")["total"] != float64(1) || h.list("linux.do")["total"] != float64(0) {
		t.Fatal("README retry leaked across tracking types")
	}
	// A successful negative qualification completes this attempt; it must clear
	// the earlier admission error without creating a Linux.do project.
	h.sources[readmeURL] = func() *http.Response { return response("No community marker") }
	h.tick(beijing("2026-10-01T09:31:00"))
	got := h.list("linux.do")
	admission := got["status"].(map[string]any)["admission"].(map[string]any)
	if got["total"] != float64(0) || admission["failed"] != float64(0) || admission["error"] != nil {
		t.Fatal("completed qualification left a retry warning", got)
	}
}

type cleanupReport struct {
	CompletedAt int64  `json:"completedAt"`
	Backup      string `json:"backup"`
	Items       []struct {
		ID, Status, Error string
		NextAt            int64 `json:"nextAt"`
	} `json:"items"`
}

func cleanupProgress(t *testing.T, h *collectionHarness) cleanupReport {
	t.Helper()
	var p cleanupReport
	if found, err := h.store.State(cleanupKey, &p); err != nil || !found {
		t.Fatal("missing cleanup progress", err)
	}
	return p
}
func copyLinuxProjects(t *testing.T, h *collectionHarness) {
	t.Helper()
	for _, sql := range []string{
		`INSERT INTO projects SELECT 'linux.do:'||repo_id,cycle||'-linux',name,description,language,topics,entered_at,listed_at,observed_at,attempted_at,error,stars,trends,metadata_at,'linux.do',repo_id,homepage FROM projects WHERE type='github'`,
		`INSERT INTO snapshot_days SELECT cycle||'-linux',day,hours FROM snapshot_days WHERE cycle NOT LIKE '%-linux'`,
		`DELETE FROM system_state WHERE key='linuxdo_cleanup_v1'`,
	} {
		if _, err := h.store.DB.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
}
func maintenance(h *collectionHarness, backup string) *tracking.Scheduler {
	return tracking.NewScheduler(h.store, tracking.SchedulerOptions{Clock: h.clock, Client: h.client, StartedAt: h.clock.at, Token: "private-token-secret", LinuxdoCleanup: true, BackupDir: backup})
}

func TestLinuxdoCleanupBackupIsolationResumeAndStartupGate(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	h.tick(beijing("2026-10-01T09:00:00"))
	copyLinuxProjects(t, h)
	githubBefore, err := h.store.ProjectRows("WHERE type='github' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	// An expired physical bucket must also remain untouched while collection is paused.
	old := beijing("2026-08-30T09:00:00")
	if _, err := h.store.DB.Exec(`INSERT INTO snapshot_days VALUES((SELECT cycle FROM projects WHERE id='github:R_0'),?,?)`, old/tracking.Day, mustJSON(map[string]any{fmtInt(old / tracking.Hour): [2]int64{old, 50}})); err != nil {
		t.Fatal(err)
	}
	before, err := tracking.Audit(h.store.DB)
	if err != nil {
		t.Fatal(err)
	}
	h.tick(beijing("2026-10-01T11:00:00"))
	gated, err := tracking.Audit(h.store.DB)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, gated) {
		t.Fatal("startup gate allowed writes before cleanup")
	}
	if h.list("linux.do")["total"] != float64(2) {
		t.Fatal("queries unavailable while gated")
	}
	firstCalls, secondCalls := 0, 0
	h.sources[readmeURL] = func() *http.Response { firstCalls++; return response("not a linux.do project") }
	h.sources["https://api.github.com/repos/acme/two/readme"] = func() *http.Response { secondCalls++; r := response("temporary"); r.StatusCode = 503; return r }
	backups := t.TempDir()
	h.scheduler = maintenance(h, backups)
	h.tick(beijing("2026-10-01T11:01:00"))
	progress := cleanupProgress(t, h)
	if progress.CompletedAt != 0 || progress.Backup == "" || progress.Items[0].Status != "removed" || progress.Items[1].Status != "" {
		t.Fatal(progress)
	}
	if _, err := os.Stat(progress.Backup); err != nil {
		t.Fatal(err)
	}
	if h.list("linux.do")["total"] != float64(1) || cacheExpiry(t, h, "R_0") == 0 {
		t.Fatal("negative result not committed with deletion")
	}
	githubAfter, err := h.store.ProjectRows("WHERE type='github' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(githubBefore, githubAfter) {
		t.Fatal("cleanup changed GitHub projects")
	}
	var historyCount int
	if err := h.store.DB.QueryRow("SELECT COUNT(*) FROM snapshot_days WHERE cycle=?", githubBefore[0].Cycle).Scan(&historyCount); err != nil || historyCount != 2 {
		t.Fatal("GitHub history was pruned", historyCount, err)
	}
	if err := h.store.DB.QueryRow("SELECT COUNT(*) FROM snapshot_days WHERE cycle=?", githubBefore[0].Cycle+"-linux").Scan(&historyCount); err != nil || historyCount != 0 {
		t.Fatal("removed Linux.do history remained", err)
	}
	// Restore the pre-cleanup snapshot; all original records exist and its gate is still closed.
	restoredPath := filepath.Join(t.TempDir(), tracking.DatabaseName)
	if err := tracking.Restore(progress.Backup, restoredPath); err != nil {
		t.Fatal(err)
	}
	restored, err := tracking.Open(restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	restoredAudit, err := tracking.Audit(restored.DB)
	if err != nil {
		t.Fatal(err)
	}
	if restoredAudit.ProjectTypes["linux.do"] != 2 {
		t.Fatal("backup taken after deletion")
	}
	scheduler := tracking.NewScheduler(restored, tracking.SchedulerOptions{Clock: h.clock, Client: h.client, StartedAt: h.clock.at - tracking.Day})
	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, found := restoredAudit.StateCheckpoints[cleanupKey]; found {
		t.Fatal("pre-cleanup backup bypasses gate")
	}
	restored.Close()
	var dbPath string
	if err := h.store.DB.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&dbPath); err != nil {
		t.Fatal(err)
	}
	h.store.Close()
	h.store, err = tracking.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.store.Close() })
	h.scheduler = maintenance(h, backups)
	h.tick(progress.Items[1].NextAt - 1)
	if firstCalls != 1 || secondCalls != 1 {
		t.Fatal("restart repeated completed or backed-off checks")
	}
	h.sources["https://api.github.com/repos/acme/two/readme"] = func() *http.Response { secondCalls++; return response("https://linux.do") }
	h.tick(progress.Items[1].NextAt)
	done := cleanupProgress(t, h)
	if done.CompletedAt == 0 || done.Backup != progress.Backup || firstCalls != 1 || secondCalls != 2 {
		t.Fatal(done, firstCalls, secondCalls)
	}
	h.tick(beijing("2026-10-01T15:00:00"))
	unchanged, err := h.store.ProjectRows("WHERE type='github' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(githubBefore, unchanged) {
		t.Fatal("maintenance resumed collection automatically")
	}
	audit, err := tracking.Audit(h.store.DB)
	if err != nil {
		t.Fatal(err)
	}
	if audit.Integrity != "ok" || audit.ForeignKeys != "ok" || audit.StateCheckpoints[cleanupKey] == nil {
		t.Fatal("cleanup report missing from audit")
	}
	h.scheduler = tracking.NewScheduler(h.store, tracking.SchedulerOptions{Clock: h.clock, Client: h.client, StartedAt: h.clock.at})
	h.tick(beijing("2026-10-01T16:59:59"))
	still, err := h.store.ProjectRows("WHERE type='github' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(githubBefore, still) {
		t.Fatal("normal restart ignored next-slot gate")
	}
	h.tick(beijing("2026-10-01T17:00:00"))
	if row, _ := h.store.ProjectRows("WHERE id='github:R_0'"); len(row) != 1 || row[0].ObservedAt < beijing("2026-10-01T17:00:00") {
		t.Fatal("collection did not resume")
	}
}

func TestLinuxdoCleanupTransactionRollbackAndBatchLimit(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	h.names = make([]string, 21)
	h.stars = make([]int64, 21)
	for i := range h.names {
		h.names[i] = fmt.Sprintf("acme/repo-%d", i)
		h.stars[i] = 100
	}
	seedMatureProjects(t, h, 21, beijing("2026-10-01T09:00:00"))
	copyLinuxProjects(t, h)
	h.sources["https://api.github.com/repos/acme/repo-0/readme"] = func() *http.Response { return response("unrelated") }
	if _, err := h.store.DB.Exec(`CREATE TRIGGER fail_cleanup BEFORE UPDATE ON system_state WHEN NEW.key='linuxdo_cleanup_v1' AND NEW.value LIKE '%"status":"removed"%' BEGIN SELECT RAISE(ABORT,'injected checkpoint failure'); END`); err != nil {
		t.Fatal(err)
	}
	h.scheduler = maintenance(h, t.TempDir())
	h.clock.at = beijing("2026-10-01T11:00:00")
	if err := h.scheduler.Tick(context.Background()); err == nil {
		t.Fatal("expected checkpoint failure")
	}
	if h.list("linux.do")["total"] != float64(21) || cacheExpiry(t, h, "R_0") != 0 {
		t.Fatal("transaction partially committed")
	}
	if p := cleanupProgress(t, h); p.Items[0].Status != "" {
		t.Fatal("failed item marked complete")
	}
	if _, err := h.store.DB.Exec("DROP TRIGGER fail_cleanup"); err != nil {
		t.Fatal(err)
	}
	h.tick(beijing("2026-10-01T11:01:00"))
	progress := cleanupProgress(t, h)
	finished := 0
	for _, item := range progress.Items {
		if item.Status != "" {
			finished++
		}
	}
	if finished != 20 || progress.CompletedAt != 0 {
		t.Fatal("batch must stop after 20 items", finished)
	}
	h.tick(beijing("2026-10-01T11:02:00"))
	if p := cleanupProgress(t, h); p.CompletedAt == 0 {
		t.Fatal("final batch did not complete")
	}
}

func TestLinuxdoCleanupRequiresBackupAndFencesReplacementCycle(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	h.names = h.names[:1]
	h.stars = h.stars[:1]
	h.tick(beijing("2026-10-01T09:00:00"))
	copyLinuxProjects(t, h)
	backupDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(backupDir, "linuxdo-cleanup-v1"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	h.scheduler = maintenance(h, backupDir)
	if err := h.scheduler.Tick(context.Background()); err == nil {
		t.Fatal("cleanup ignored invalid backup directory")
	}
	if h.list("linux.do")["total"] != float64(1) {
		t.Fatal("deleted without a backup")
	}
	var progress cleanupReport
	if found, err := h.store.State(cleanupKey, &progress); err != nil || found {
		t.Fatal("initialized cleanup despite failed backup", err)
	}
	if err := os.Remove(filepath.Join(backupDir, "linuxdo-cleanup-v1")); err != nil {
		t.Fatal(err)
	}
	h.sources[readmeURL] = func() *http.Response { r := response("temporary"); r.StatusCode = 503; return r }
	h.tick(beijing("2026-10-01T11:00:00"))
	progress = cleanupProgress(t, h)
	// Simulate an unexpected external replacement after the frozen inventory was taken.
	if _, err := h.store.DB.Exec(`DELETE FROM projects WHERE type='linux.do'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.DB.Exec(`INSERT INTO projects SELECT 'linux.do:'||repo_id,'replacement',name,description,language,topics,entered_at,listed_at,observed_at,attempted_at,error,stars,trends,metadata_at,'linux.do',repo_id,homepage FROM projects WHERE type='github'`); err != nil {
		t.Fatal(err)
	}
	h.sources[readmeURL] = func() *http.Response { return response("no marker") }
	h.clock.at = progress.Items[0].NextAt
	if err := h.scheduler.Tick(context.Background()); err == nil {
		t.Fatal("cleanup ignored changed tracking cycle")
	}
	rows, err := h.store.ProjectRows("WHERE type='linux.do'")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Cycle != "replacement" || cacheExpiry(t, h, "R_0") != 0 || cleanupProgress(t, h).CompletedAt != 0 {
		t.Fatal("old cleanup mutated replacement cycle")
	}
}
