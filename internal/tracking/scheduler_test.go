package tracking_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lizhian/github-trending-tracking/internal/tracking"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type testClock struct{ at int64 }

func (c *testClock) Now() int64 { return c.at }
func (c *testClock) Sleep(ctx context.Context, d time.Duration) error {
	c.at += d.Milliseconds()
	return ctx.Err()
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}
func newStore(t *testing.T) *tracking.Store {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tracking.sqlite")
	if e := tracking.Initialize(p); e != nil {
		t.Fatal(e)
	}
	s, e := tracking.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func beijing(s string) int64 {
	t, e := time.Parse(time.RFC3339, s+"+08:00")
	if e != nil {
		panic(e)
	}
	return t.UnixMilli()
}
func requestJSON(t *testing.T, s *tracking.Store, c *testClock, path string) map[string]any {
	t.Helper()
	h := tracking.Handler(s, tracking.HTTPOptions{Now: func() time.Time { return time.UnixMilli(c.at) }})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var v map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
		t.Fatal(e)
	}
	return v
}
func TestSchedulerWaitsForNextSlotAndAdmitsWithActualObservation(t *testing.T) {
	s := newStore(t)
	c := &testClock{beijing("2026-10-01T11:10:00")}
	start := c.at
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.github.com" {
			b, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(b), "stargazerCount") {
				return response(`{"data":{"r0":{"id":"R_1","nameWithOwner":"acme/one"}}}`), nil
			}
			return response(`{"data":{"nodes":[{"id":"R_1","nameWithOwner":"acme/one","stargazerCount":100,"repositoryTopics":{"nodes":[]}}]}}`), nil
		}
		if strings.Contains(r.URL.Path, "github/trending/daily") {
			return response(`<rss><channel><item><link>https://github.com/acme/one</link></item></channel></rss>`), nil
		}
		return response(`<rss><channel/></rss>`), nil
	})}
	scheduler := tracking.NewScheduler(s, tracking.SchedulerOptions{Clock: c, Client: client, StartedAt: start, Token: "test-secret"})
	if e := scheduler.Tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	if got := requestJSON(t, s, c, "/api/projects"); got["total"] != float64(0) {
		t.Fatal(got)
	}
	c.at = beijing("2026-10-01T11:59:59")
	if e := scheduler.Tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	if got := requestJSON(t, s, c, "/api/projects"); got["total"] != float64(0) {
		t.Fatal(got)
	}
	c.at = beijing("2026-10-01T12:00:00")
	tick := c.at
	if e := scheduler.Tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	got := requestJSON(t, s, c, "/api/projects")
	if got["total"] != float64(1) {
		t.Fatal(got)
	}
	item := got["items"].([]any)[0].(map[string]any)
	if item["observedAt"] != float64(tick) || item["enteredAt"] != float64(tick) || item["stars"] != float64(100) {
		t.Fatal(item)
	}
	if strings.Contains(mustJSON(got), "test-secret") {
		t.Fatal("secret leaked")
	}
	history := requestJSON(t, s, c, "/api/projects/github:R_1/history")
	if len(history["points"].([]any)) != 1 {
		t.Fatal(history)
	}
}
func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

type collectionHarness struct {
	t              *testing.T
	store          *tracking.Store
	clock          *testClock
	scheduler      *tracking.Scheduler
	client         *http.Client
	sources        map[string]func() *http.Response
	names          []string
	stars          []int64
	failed         map[string]bool
	graphqlFailure bool
	seenQueries    []string
	homepages      map[int]any
}

func newCollectionHarness(t *testing.T, start int64) *collectionHarness {
	h := &collectionHarness{t: t, store: newStore(t), clock: &testClock{start}, sources: map[string]func() *http.Response{}, names: []string{"acme/one", "acme/two"}, stars: []int64{100, 200}, failed: map[string]bool{}}
	// Existing collection scenarios model a database already cleared for normal operation.
	if err := h.store.SetState("linuxdo_cleanup_v1", map[string]any{"completedAt": start}); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if f := h.sources[r.URL.String()]; f != nil {
			return f(), nil
		}
		if r.URL.Host == "api.github.com" && strings.HasSuffix(r.URL.Path, "/readme") {
			return response("Community: https://linux.do"), nil
		}
		if r.URL.Host != "api.github.com" {
			if strings.Contains(r.URL.Path, "raw.githubusercontent") {
				return response(""), nil
			}
			if strings.Contains(r.URL.Path, "github/trending/daily") {
				links := ""
				for _, n := range h.names {
					links += "<item><link>https://github.com/" + n + "</link></item>"
				}
				return response("<rss><channel>" + links + "</channel></rss>"), nil
			}
			return response("<rss><channel/></rss>"), nil
		}
		b, _ := io.ReadAll(r.Body)
		var payload map[string]string
		_ = json.Unmarshal(b, &payload)
		q := payload["query"]
		h.seenQueries = append(h.seenQueries, q)
		if h.graphqlFailure {
			res := response("private-token-secret-body")
			res.StatusCode = 503
			return res, nil
		}
		makeNode := func(i int) any {
			if h.failed[fmt.Sprintf("R_%d", i)] {
				return nil
			}
			v := map[string]any{"id": fmt.Sprintf("R_%d", i)}
			if strings.Contains(q, "nameWithOwner") {
				v["nameWithOwner"] = h.names[i]
			}
			if strings.Contains(q, "stargazerCount") {
				v["stargazerCount"] = h.stars[i]
			}
			if strings.Contains(q, "homepageUrl") {
				v["homepageUrl"] = h.homepages[i]
			}
			if strings.Contains(q, "repositoryTopics") {
				v["repositoryTopics"] = map[string]any{"nodes": []any{}}
				v["primaryLanguage"] = map[string]any{"name": "Go"}
			}
			return v
		}
		data := map[string]any{}
		if m := regexp.MustCompile(`ids:(\[[^\]]*\])`).FindStringSubmatch(q); m != nil {
			var ids []string
			_ = json.Unmarshal([]byte(m[1]), &ids)
			nodes := []any{}
			for _, id := range ids {
				var i int
				_, _ = fmt.Sscanf(id, "R_%d", &i)
				nodes = append(nodes, makeNode(i))
			}
			data["nodes"] = nodes
		} else {
			matches := regexp.MustCompile(`r([0-9]+): repository\(owner:("[^"]*"),name:("[^"]*")\)`).FindAllStringSubmatch(q, -1)
			for _, m := range matches {
				var owner, name string
				_ = json.Unmarshal([]byte(m[2]), &owner)
				_ = json.Unmarshal([]byte(m[3]), &name)
				var v any
				for i, n := range h.names {
					if strings.EqualFold(n, owner+"/"+name) {
						v = makeNode(i)
					}
				}
				data["r"+m[1]] = v
			}
		}
		return response(mustJSON(map[string]any{"data": data})), nil
	})}
	h.client = client
	h.scheduler = tracking.NewScheduler(h.store, tracking.SchedulerOptions{Clock: h.clock, Client: client, StartedAt: start, Token: "private-token-secret", FallbackURL: "https://fallback.example/rss?secret=private"})
	return h
}
func (h *collectionHarness) tick(at int64) {
	h.t.Helper()
	h.clock.at = at
	if e := h.scheduler.Tick(context.Background()); e != nil {
		h.t.Fatal(e)
	}
}
func (h *collectionHarness) list(typ string) map[string]any {
	return requestJSON(h.t, h.store, h.clock, "/api/projects?type="+typ)
}
func (h *collectionHarness) history(id string) map[string]any {
	return requestJSON(h.t, h.store, h.clock, "/api/projects/"+id+"/history?window=720")
}

func TestAttachmentLinksNeverBecomeCandidatesAndOldPendingIsPersistentlyRemoved(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	h.sources["https://linux.do/c/develop/4.rss"] = func() *http.Response {
		return response(`<rss><channel><item><description>https://github.com/user-attachments/assets/image-id https://github.com/acme/one</description></item></channel></rss>`)
	}
	first := beijing("2026-10-01T09:00:00")
	h.tick(first)
	if got := h.list("linux.do"); got["total"] != float64(1) {
		t.Fatal("normal repository was filtered", got)
	}
	// No source is due on the next tick: cleaning old pending must itself persist.
	if err := h.store.SetState("pending", []map[string]any{{"type": "linux.do", "name": "USER-ATTACHMENTS/assets", "nextAt": first + tracking.Day}}); err != nil {
		t.Fatal(err)
	}
	h.tick(first + 60_000)
	var pending []map[string]any
	if _, err := h.store.State("pending", &pending); err != nil || len(pending) != 0 {
		t.Fatal("attachment persisted in pending", pending, err)
	}
	if strings.Contains(strings.ToLower(mustJSON(h.seenQueries)), "user-attachments") {
		t.Fatal("attachment reached GitHub queries", h.seenQueries)
	}
}

func TestCandidateAndSamplingFailuresHaveIndependentStatus(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	first := beijing("2026-10-01T09:00:00")
	h.tick(first)
	if err := h.store.SetState("pending", []map[string]any{{"type": "github", "name": "acme/missing"}}); err != nil {
		t.Fatal(err)
	}
	h.tick(first + 5*60_000)
	status := h.list("github")["status"].(map[string]any)
	admission, ok := status["admission"].(map[string]any)
	if !ok || admission["failed"] != float64(1) {
		t.Fatal("missing candidate not reported as admission failure", status)
	}
	if sampling, ok := status["collection"].(map[string]any); ok && sampling["failed"] != float64(0) {
		t.Fatal("candidate failure became update failure", status)
	}
	h.failed["R_0"] = true
	h.tick(beijing("2026-10-01T11:00:00"))
	status = h.list("github")["status"].(map[string]any)
	if status["collection"].(map[string]any)["failed"] != float64(1) || status["admission"].(map[string]any)["failed"] != float64(1) {
		t.Fatal("sampling and admission errors mixed", status)
	}
	h.failed["R_0"] = false
	retryAt := beijing("2026-10-01T11:31:00")
	h.tick(retryAt)
	status = h.list("github")["status"].(map[string]any)
	if status["collection"].(map[string]any)["failed"] != float64(0) || status["admission"].(map[string]any)["failed"] != float64(1) {
		t.Fatal("sampling recovery cleared the candidate failure or retained its own error", status)
	}
	var pending []map[string]any
	if _, err := h.store.State("pending", &pending); err != nil || len(pending) != 1 || pending[0]["nextAt"].(float64) < float64(retryAt+5*60_000) {
		t.Fatal("missing candidate lost its retry", pending, err)
	}
}

func TestLegacyMixedStatusUsesActualProjectErrorsBeforeNextSamplingSlot(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	first := beijing("2026-10-01T09:00:00")
	h.tick(first)
	if _, err := h.store.DB.Exec("DELETE FROM system_state WHERE key LIKE 'admission%'"); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]any{"attemptedAt": first + 5*60_000, "succeededAt": first, "succeeded": 0, "failed": 2, "error": "GitHub repository unavailable"}
	if err := h.store.SetState("pending", []map[string]any{{"name": "acme/missing-one"}, {"name": "acme/missing-two"}, {"name": "user-attachments/assets"}}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetState("collection:github", legacy); err != nil {
		t.Fatal(err)
	}
	status := h.list("github")["status"].(map[string]any)
	if status["collection"].(map[string]any)["failed"] != float64(0) || status["admission"].(map[string]any)["failed"] != float64(2) {
		t.Fatal("legacy candidates still trigger update failure", status)
	}
	if _, err := h.store.DB.Exec("UPDATE projects SET error='GitHub HTTP 503',attempted_at=? WHERE id='github:R_0'", first+5*60_000); err != nil {
		t.Fatal(err)
	}
	// A filter hiding the failing project must not hide the type's update error.
	status = requestJSON(t, h.store, h.clock, "/api/projects?type=github&q=two")["status"].(map[string]any)
	if status["collection"].(map[string]any)["failed"] != float64(1) || status["collection"].(map[string]any)["error"] != "GitHub HTTP 503" || status["admission"].(map[string]any)["failed"] != float64(1) {
		t.Fatal("legacy compatibility hid a real sampling failure", status)
	}
	if err := h.store.SetState("pending", []map[string]any{{"name": "user-attachments/assets"}}); err != nil {
		t.Fatal(err)
	}
	status = h.list("github")["status"].(map[string]any)
	if status["admission"] != nil || status["collection"].(map[string]any)["failed"] != float64(1) {
		t.Fatal("legacy attachment error survived or real project error was cleared", status)
	}
}

func TestSharedObservationKeepsIndependentHistoryAndAdmissionTime(t *testing.T) {
	start := beijing("2026-10-01T08:50:00")
	h := newCollectionHarness(t, start)
	h.homepages = map[int]any{0: "https://example.com/one", 1: "https://example.com/two"}
	linux := "https://linux.do/c/develop/4.rss"
	fallback := "https://fallback.example/rss?secret=private"
	unavailable := func() *http.Response { r := response("private-source-body"); r.StatusCode = 503; return r }
	h.sources[linux] = unavailable
	h.sources[fallback] = unavailable
	h.tick(beijing("2026-10-01T09:00:00"))
	h.stars[0] = 150
	h.tick(beijing("2026-10-01T11:00:00"))
	h.sources[linux] = func() *http.Response {
		return response(`<rss xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><item><description><![CDATA[https://github.com/acme/one/tree/main https://github.com/ACME/one.git]]></description><content:encoded>https://github.com/acme/one#readme</content:encoded></item></channel></rss>`)
	}
	h.tick(beijing("2026-10-01T11:30:00"))
	item := h.list("linux.do")["items"].([]any)[0].(map[string]any)
	if item["enteredAt"] != float64(beijing("2026-10-01T11:30:00")) || item["observedAt"] != float64(beijing("2026-10-01T11:00:00")) {
		t.Fatal(item)
	}
	points := h.history("linux.do:R_0")["points"].([]any)
	if len(points) != 1 || points[0].([]any)[1] != float64(150) {
		t.Fatal(points)
	}
	if len(h.history("github:R_0")["points"].([]any)) != 2 {
		t.Fatal("copied history")
	}
	h.stars[0] = 175
	h.tick(beijing("2026-10-01T13:00:00"))
	item = h.list("linux.do")["items"].([]any)[0].(map[string]any)
	if item["stars"] != float64(175) || item["trends"].(map[string]any)["24"].(map[string]any)["delta"] != float64(25) {
		t.Fatal(item)
	}
	// A stars-only daily metadata interval can still admit a newly found type.
	h.sources[linux] = func() *http.Response {
		return response(`<rss><channel><item><description>https://github.com/acme/two</description></item></channel></rss>`)
	}
	h.tick(beijing("2026-10-01T15:00:00"))
	for _, v := range h.list("linux.do")["items"].([]any) {
		item := v.(map[string]any)
		if item["name"] == "" || item["language"] != "Go" || item["homepage"] != "https://example.com/"+strings.TrimPrefix(item["name"].(string), "acme/") {
			t.Fatal(item)
		}
	}
	if strings.Contains(mustJSON(h.list("linux.do")), "secret") {
		t.Fatal("secret leaked")
	}
}
func TestFailedAdmissionSharesRetryAcrossNewTypesAndRediscovery(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T10:50:00"))
	h.names = h.names[:1]
	h.stars = h.stars[:1]
	h.failed["R_0"] = true
	first := beijing("2026-10-01T11:00:00")
	h.tick(first)
	if h.list("github")["total"] != float64(0) {
		t.Fatal("failed admission created project")
	}
	// The new type is persisted while no tracked project exists, as it can be after discovery.
	if e := h.store.SetState("pending", []map[string]any{{"name": "acme/one", "repoId": "R_0", "nextAt": first + 30*60_000}, {"type": "linux.do", "name": "acme/one"}}); e != nil {
		t.Fatal(e)
	}
	h.failed["R_0"] = false
	h.tick(first + 5*60_000)
	if h.list("linux.do")["total"] != float64(0) {
		t.Fatal("bypassed shared retry")
	}
	h.tick(first + 29*60_000)
	if h.list("github")["total"] != float64(0) {
		t.Fatal("retried early")
	}
	h.tick(first + 30*60_000)
	for _, typ := range []string{"github", "linux.do"} {
		got := h.list(typ)
		if got["total"] != float64(1) {
			t.Fatal(got)
		}
		item := got["items"].([]any)[0].(map[string]any)
		if item["enteredAt"] != float64(first+30*60_000) || item["stars"] != float64(100) {
			t.Fatal(item)
		}
	}
}
func TestRestartAcrossMidnightRetainsLongHostCooldownAndNeverRevivesOldData(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T20:50:00"))
	h.tick(beijing("2026-10-01T21:00:00"))
	old := h.history("github:R_0")["points"]
	// Persisted upstream headers can require a longer wait than the restart gate.
	until := beijing("2026-10-02T06:00:00")
	if e := h.store.SetState("upstream", map[string]any{"api.github.com": map[string]any{"startedAt": 0, "blockedUntil": until}}); e != nil {
		t.Fatal(e)
	}
	h.clock.at = beijing("2026-10-01T21:10:00")
	h.scheduler = tracking.NewScheduler(h.store, tracking.SchedulerOptions{Clock: h.clock, Client: h.client, StartedAt: h.clock.at, Token: "private-token-secret"})
	h.tick(beijing("2026-10-01T23:00:00"))
	h.tick(beijing("2026-10-02T01:00:00"))
	h.tick(beijing("2026-10-02T05:00:00"))
	history := h.history("github:R_0")
	if mustJSON(history["points"]) != mustJSON(old) || history["stale"] != true {
		t.Fatal(history)
	}
	h.stars[0] = 90
	h.tick(until)
	history = h.history("github:R_0")
	if len(history["points"].([]any)) != 2 || history["trend"].(map[string]any)["delta"] != float64(-10) {
		t.Fatal(history)
	}
	// Restart exactly on the slot waits strictly until the next scheduled time.
	h.clock.at = beijing("2026-10-02T09:00:00")
	h.scheduler = tracking.NewScheduler(h.store, tracking.SchedulerOptions{Clock: h.clock, Client: h.client, StartedAt: h.clock.at})
	h.tick(h.clock.at)
	if len(h.history("github:R_0")["points"].([]any)) != 2 {
		t.Fatal("exact slot was replayed")
	}
	h.tick(beijing("2026-10-02T11:00:00"))
	if len(h.history("github:R_0")["points"].([]any)) != 3 {
		t.Fatal("next slot not collected")
	}
}
func TestNewestOnlyCollectionKeepsGapAndFailuresKeepGoodObservations(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	h.tick(beijing("2026-10-01T09:00:00"))
	h.stars[0] = 130
	h.tick(beijing("2026-10-01T15:07:00"))
	history := h.history("github:R_0")
	if len(history["points"].([]any)) != 2 || len(history["gaps"].([]any)) != 1 {
		t.Fatal(history)
	}
	before := mustJSON(history["points"])
	h.graphqlFailure = true
	h.tick(beijing("2026-10-01T17:00:00"))
	history = h.history("github:R_0")
	if mustJSON(history["points"]) != before {
		t.Fatal(history)
	}
	got := h.list("github")
	if strings.Contains(mustJSON(got), "private-") {
		t.Fatal("upstream detail leaked")
	}
	h.graphqlFailure = false
	h.stars[0] = 140
	h.tick(beijing("2026-10-01T17:04:00"))
	if mustJSON(h.history("github:R_0")["points"]) != before {
		t.Fatal("ignored backoff")
	}
	h.tick(beijing("2026-10-01T17:06:00"))
	if len(h.history("github:R_0")["points"].([]any)) != 3 {
		t.Fatal("retry failed")
	}
}
func seedMatureProjects(t *testing.T, h *collectionHarness, count int, at int64) {
	t.Helper()
	tx, e := h.store.DB.Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("R_%d", i)
		name := fmt.Sprintf("acme/repo-%d", i)
		stars := int64(200)
		if i < 4 {
			stars = 100
		}
		entered := at - tracking.Day
		if i == count-1 {
			entered = at - tracking.Hour
		}
		trends := map[string]any{}
		for _, w := range []string{"24", "72", "168", "720"} {
			trends[w] = map[string]any{"start": at - tracking.Day, "end": at, "delta": stars - 100, "state": "partial", "gaps": []any{}}
		}
		var errorValue any
		if i == 0 {
			errorValue = "GitHub repository unavailable"
		}
		if _, e = tx.Exec(`INSERT INTO projects(id,type,repo_id,cycle,name,topics,entered_at,observed_at,attempted_at,error,stars,trends,metadata_at) VALUES(?,'github',?,?,?,'[]',?,?,?,?,?,?,?)`, "github:"+id, id, "cycle-"+id, name, entered, at, at, errorValue, stars, mustJSON(trends), at); e != nil {
			t.Fatal(e)
		}
		for _, p := range [][2]int64{{at - tracking.Day, 100}, {at, stars}} {
			if _, e = tx.Exec(`INSERT INTO snapshot_days(cycle,day,hours) VALUES(?,?,?)`, "cycle-"+id, p[0]/tracking.Day, mustJSON(map[string]any{fmt.Sprint(p[0] / tracking.Hour): p})); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
}
func TestCleanupRunsDuringStartupWaitProtectsYoungProjectsAndReentryStartsNewHistory(t *testing.T) {
	start := beijing("2026-10-02T03:50:00")
	h := newCollectionHarness(t, start)
	h.names = make([]string, 504)
	h.stars = make([]int64, 504)
	for i := range h.names {
		h.names[i] = fmt.Sprintf("acme/repo-%d", i)
		h.stars[i] = 200
	}
	observed := beijing("2026-10-02T01:00:00")
	seedMatureProjects(t, h, 504, observed)
	if e := h.store.SetState("sampling_policy", map[string]any{"version": 2, "effectiveAt": observed - 7*tracking.Day}); e != nil {
		t.Fatal(e)
	}
	h.tick(beijing("2026-10-02T04:00:00"))
	got := h.list("github")
	if got["total"] != float64(500) {
		t.Fatal(got["total"], got["status"])
	}
	for i := 0; i < 4; i++ {
		w := httptest.NewRecorder()
		tracking.Handler(h.store, tracking.HTTPOptions{}).ServeHTTP(w, httptest.NewRequest("GET", fmt.Sprintf("/api/projects/github:R_%d/history", i), nil))
		if w.Code != 404 {
			t.Fatal("lowest eligible remained", i, w.Code)
		}
	}
	if len(h.history("github:R_503")["points"].([]any)) != 2 {
		t.Fatal("young project removed")
	}
	h.tick(beijing("2026-10-02T05:00:00"))
	history := h.history("github:R_0")
	if len(history["points"].([]any)) != 1 || history["trend"].(map[string]any)["delta"] != nil {
		t.Fatal("reentry inherited old cycle", history)
	}
}
func TestCleanupOrdersByScoreDeltaStarsEntryTimeAndID(t *testing.T) {
	for _, tc := range []struct {
		name      string
		deltas    [2][4]int64
		stars     [2]int64
		ageDays   [2]int64
		removedID string
	}{
		{"score before delta", [2][4]int64{{1, 0, 0, 0}, {0, 10, 10, 10}}, [2]int64{200, 100}, [2]int64{2, 3}, "github:R_0"},
		{"delta before stars", [2][4]int64{{-2, 0, 0, 0}, {-1, 0, 0, 0}}, [2]int64{200, 100}, [2]int64{2, 3}, "github:R_0"},
		{"stars before entry time", [2][4]int64{}, [2]int64{200, 100}, [2]int64{3, 2}, "github:R_1"},
		{"entry time before ID", [2][4]int64{}, [2]int64{100, 100}, [2]int64{2, 3}, "github:R_1"},
		{"stable ID", [2][4]int64{}, [2]int64{100, 100}, [2]int64{2, 2}, "github:R_0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCollectionHarness(t, beijing("2026-10-02T03:50:00"))
			observed := beijing("2026-10-02T01:00:00")
			seedMatureProjects(t, h, 501, observed)
			if err := h.store.SetState("sampling_policy", map[string]any{"version": 2, "effectiveAt": observed - 7*tracking.Day}); err != nil {
				t.Fatal(err)
			}
			trends := func(deltas [4]int64) string {
				values := map[string]any{}
				for i, width := range tracking.Windows {
					values[fmt.Sprint(width)] = map[string]any{"start": observed - tracking.Day, "end": observed, "delta": deltas[i], "gaps": []any{}}
				}
				return mustJSON(values)
			}
			if _, err := h.store.DB.Exec("UPDATE projects SET trends=?", trends([4]int64{100, 100, 100, 100})); err != nil {
				t.Fatal(err)
			}
			for i := range 2 {
				if _, err := h.store.DB.Exec("UPDATE projects SET trends=?,stars=?,entered_at=? WHERE id=?", trends(tc.deltas[i]), tc.stars[i], observed-tc.ageDays[i]*tracking.Day, fmt.Sprintf("github:R_%d", i)); err != nil {
					t.Fatal(err)
				}
			}
			h.tick(beijing("2026-10-02T04:00:00"))
			rows, err := h.store.ProjectRows("")
			if err != nil || len(rows) != 500 {
				t.Fatal("cleanup did not reach capacity", len(rows), err)
			}
			for _, row := range rows {
				if row.ID == tc.removedID {
					t.Fatal("wrong project removed", tc.removedID)
				}
			}
		})
	}
}

func TestLinuxPrimaryFallbackAndAuthoritativeSourceParsing(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	h.names = []string{"acme/one", "acme/two", "acme/dependency", "acme/detail"}
	h.stars = []int64{100, 200, 300, 400}
	primary := "https://linux.do/c/develop/4.rss"
	fallback := "https://fallback.example/rss?secret=private"
	h.sources[primary] = func() *http.Response {
		res := response("private-body")
		res.StatusCode = 429
		res.Header.Set("Retry-After", "7200")
		return res
	}
	h.sources[fallback] = func() *http.Response {
		return response(`<rss xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><item><link>https://linux.do/t/topic/123</link><description><![CDATA[https://github.com/acme/one/tree/main https://github.com.evil.com/no/repo]]></description><content:encoded>&lt;a href=&quot;https://github.com/acme/dependency/issues/1&quot;&gt;Dependency&lt;/a&gt;</content:encoded></item></channel></rss>`)
	}
	h.sources["https://rsshub-balancer.virworks.moe/github/trending/daily/any"] = func() *http.Response {
		return response(`<rss><channel><item><link>https://github.com/acme/one</link><description>https://github.com/acme/dependency</description></item></channel></rss>`)
	}
	detail := "https://hellogithub.com/repository/abc123"
	h.sources["https://rsshub-balancer.virworks.moe/hellogithub/home/all"] = func() *http.Response {
		return response(`<rss><channel><item><guid isPermaLink="true">` + detail + `</guid></item></channel></rss>`)
	}
	h.sources[detail] = func() *http.Response {
		return response(`<a href="https://github.com/acme/two">recommendation</a><script id="__NEXT_DATA__">{"props":{"pageProps":{"repo":{"url":"https://github.com/acme/detail"}}}}</script>`)
	}
	h.tick(beijing("2026-10-01T09:00:00"))
	linux := h.list("linux.do")
	if linux["total"] != float64(2) {
		t.Fatal(linux)
	}
	github := h.list("github")
	if github["total"] != float64(2) {
		t.Fatal(github)
	}
	if strings.Contains(mustJSON(linux), "secret") || strings.Contains(mustJSON(linux), "fallback.example") {
		t.Fatal("RSS configuration leaked")
	}
	// A valid empty primary is success and must not read the populated fallback.
	h.sources[primary] = func() *http.Response { return response(`<rss><channel/></rss>`) }
	h.sources[fallback] = func() *http.Response { t.Fatal("fallback ran despite valid primary"); return nil }
	h.tick(beijing("2026-10-01T11:01:00"))
	status := h.list("linux.do")["status"].(map[string]any)["discovery"].(map[string]any)
	if status["failed"] != float64(0) {
		t.Fatal(status)
	}
}
func TestDiscoveryBudgetDefersHelloDetailsWithoutFailures(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	h.names = []string{"acme/one"}
	h.stars = []int64{100}
	links := ""
	for i := 0; i < 21; i++ {
		page := fmt.Sprintf("https://hellogithub.com/repository/%x", i)
		links += "<item><link>" + page + "</link></item>"
		h.sources[page] = func() *http.Response {
			h.clock.at += 15_000
			return response(`<script id="__NEXT_DATA__">{"props":{"pageProps":{"repo":{"url":"https://github.com/acme/one"}}}}</script>`)
		}
	}
	h.sources["https://rsshub-balancer.virworks.moe/hellogithub/home/all"] = func() *http.Response { return response("<rss><channel>" + links + "</channel></rss>") }
	first := beijing("2026-10-01T09:00:00")
	h.tick(first)
	if h.clock.at-first > 3*60_000+40_000 {
		t.Fatal("budget exceeded", h.clock.at-first)
	}
	status := h.list("github")["status"].(map[string]any)["discovery"].(map[string]any)
	if status["failed"] != float64(0) {
		t.Fatal("budget became failure", status)
	}
	// Remaining details finish in subsequent ticks.
	h.tick(first + 10*60_000)
	h.tick(first + 20*60_000)
	got := h.list("github")
	if got["total"] != float64(1) {
		t.Fatal(got)
	}
}
func TestHundredRepositoryBatchesUpdateBothTypesAndCompletedRoundStaysIdle(t *testing.T) {
	start := beijing("2026-10-02T10:50:00")
	h := newCollectionHarness(t, start)
	h.names = make([]string, 500)
	h.stars = make([]int64, 500)
	for i := range h.names {
		h.names[i] = fmt.Sprintf("acme/repo-%d", i)
		h.stars[i] = 300
	}
	observed := beijing("2026-10-02T09:00:00")
	seedMatureProjects(t, h, 500, observed)
	if _, e := h.store.DB.Exec(`INSERT INTO projects SELECT 'linux.do:'||repo_id,cycle||'-linux',name,description,language,topics,entered_at,observed_at,attempted_at,error,stars,trends,metadata_at,'linux.do',repo_id,homepage FROM projects WHERE type='github'`); e != nil {
		t.Fatal(e)
	}
	if _, e := h.store.DB.Exec(`INSERT INTO snapshot_days SELECT cycle||'-linux',day,hours FROM snapshot_days WHERE cycle NOT LIKE '%-linux'`); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 5; i++ {
		h.tick(beijing("2026-10-02T11:00:00") + int64(i)*60_000)
		for _, typ := range []string{"github", "linux.do"} {
			got := h.list(typ)
			collection := got["status"].(map[string]any)["collection"].(map[string]any)
			if collection["succeeded"] != float64(100) || collection["failed"] != float64(0) {
				t.Fatal(typ, collection)
			}
		}
	}
	for _, typ := range []string{"github", "linux.do"} {
		got := h.list(typ)
		if got["total"] != float64(500) || got["summary"].(map[string]any)["fresh"] != float64(500) {
			t.Fatal(typ, got["total"], got["summary"])
		}
		for i := 0; i < 500; i += 99 {
			history := h.history(fmt.Sprintf("%s:R_%d", typ, i))
			points := history["points"].([]any)
			if len(points) != 3 || points[2].([]any)[1] != float64(300) {
				t.Fatal(history)
			}
		}
	}
	before := mustJSON(h.list("github")["status"])
	h.tick(beijing("2026-10-02T11:10:00"))
	if mustJSON(h.list("github")["status"]) != before {
		t.Fatal("completed idle round modified status")
	}
}
func TestPartialGraphQLResultsSurviveRateLimitsAndRespectLongerRetryAfter(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	h.tick(beijing("2026-10-01T09:00:00"))
	base := h.client.Transport
	h.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.github.com" {
			b, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(b)))
			if strings.Contains(string(b), "stargazerCount") {
				res := response(`{"data":{"nodes":[{"id":"R_0","stargazerCount":125},null]},"errors":[{"type":"RATE_LIMITED"}]}`)
				res.Header.Set("Retry-After", "10800")
				return res, nil
			}
		}
		return base.RoundTrip(r)
	})
	h.tick(beijing("2026-10-01T11:00:00"))
	if len(h.history("github:R_0")["points"].([]any)) != 2 || len(h.history("github:R_1")["points"].([]any)) != 1 {
		t.Fatal("partial successes discarded")
	}
	h.client.Transport = base
	h.tick(beijing("2026-10-01T13:00:00"))
	if len(h.history("github:R_1")["points"].([]any)) != 1 {
		t.Fatal("longer upstream cooldown ignored")
	}
	h.tick(beijing("2026-10-01T14:01:00"))
	if len(h.history("github:R_1")["points"].([]any)) != 2 {
		t.Fatal("cooldown recovery failed")
	}
}
func TestLateObservationCannotWriteIntoReplacementTrackingCycle(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	h.tick(beijing("2026-10-01T09:00:00"))
	base := h.client.Transport
	replaced := false
	h.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.github.com" && !replaced {
			replaced = true
			at := h.clock.at
			row, e := h.store.ProjectRows("WHERE id='github:R_0'")
			if e != nil {
				t.Fatal(e)
			}
			v := row[0]
			trends := map[string]any{}
			for _, w := range []string{"24", "72", "168", "720"} {
				trends[w] = map[string]any{"start": at, "end": at, "delta": nil, "state": "accumulating", "gaps": []any{}}
			}
			tx, e := h.store.DB.Begin()
			if e != nil {
				t.Fatal(e)
			}
			if _, e = tx.Exec(`DELETE FROM projects WHERE id='github:R_0'`); e != nil {
				t.Fatal(e)
			}
			if _, e = tx.Exec(`INSERT INTO projects(id,cycle,name,description,language,topics,entered_at,observed_at,attempted_at,error,stars,trends,metadata_at,type,repo_id) VALUES(?,'replacement',?,?,?,?,?,?,?,NULL,999,?,?,?,?)`, v.ID, v.Name, v.Description, v.Language, v.Topics, at, at, at, mustJSON(trends), at, v.Type, v.RepoID); e != nil {
				t.Fatal(e)
			}
			if _, e = tx.Exec(`INSERT INTO snapshot_days VALUES('replacement',?,?)`, at/tracking.Day, mustJSON(map[string]any{fmt.Sprint(at / tracking.Hour): [2]int64{at, 999}})); e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(); e != nil {
				t.Fatal(e)
			}
		}
		return base.RoundTrip(r)
	})
	h.stars[0] = 120
	h.tick(beijing("2026-10-01T11:00:00"))
	history := h.history("github:R_0")
	points := history["points"].([]any)
	if len(points) != 1 || points[0].([]any)[1] != float64(999) {
		t.Fatal("old result overwrote new cycle", history)
	}
}

func TestHomepageFollowsMetadataRefresh(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	h.names = h.names[:1]
	h.homepages = map[int]any{0: "https://example.com/first"}
	check := func(want any) {
		t.Helper()
		item := h.list("github")["items"].([]any)[0].(map[string]any)
		if item["homepage"] != want {
			t.Fatalf("homepage = %v, want %v", item["homepage"], want)
		}
	}
	h.tick(beijing("2026-10-01T09:00:00"))
	check("https://example.com/first")
	h.homepages[0] = "https://example.com/updated"
	h.seenQueries = nil
	h.tick(beijing("2026-10-01T11:00:00"))
	check("https://example.com/first")
	for _, query := range h.seenQueries {
		if strings.Contains(query, "homepageUrl") {
			t.Fatal("homepage caused an early metadata request", query)
		}
	}
	h.tick(beijing("2026-10-02T11:00:00"))
	check("https://example.com/updated")
	h.homepages[0] = nil
	h.tick(beijing("2026-10-03T13:00:00"))
	check(nil)
	h.homepages[0] = ""
	h.tick(beijing("2026-10-04T15:00:00"))
	check("")
}
