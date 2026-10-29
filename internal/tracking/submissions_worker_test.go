package tracking

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type submissionTestClock struct {
	mu sync.Mutex
	at int64
}

func (c *submissionTestClock) Now() int64       { c.mu.Lock(); defer c.mu.Unlock(); return c.at }
func (c *submissionTestClock) advance(at int64) { c.mu.Lock(); c.at = at; c.mu.Unlock() }
func (c *submissionTestClock) Sleep(ctx context.Context, d time.Duration) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	c.mu.Lock()
	c.at += d.Milliseconds()
	c.mu.Unlock()
	return nil
}

type submissionTransport func(*http.Request) (*http.Response, error)

func (f submissionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func submissionResponse(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

const submissionRepository = `{"id":"R1","nameWithOwner":"acme/tool","description":"a tool","homepageUrl":"https://example.com/tool","primaryLanguage":{"name":"Go"},"repositoryTopics":{"nodes":[]},"stargazerCount":100,"isPrivate":false}`

type submissionHarness struct {
	store     *Store
	scheduler *Scheduler
	worker    *SubmissionWorker
	clock     *submissionTestClock
	requests  int
	readmes   int
	response  func(*http.Request) (*http.Response, error)
}

func newSubmissionHarness(t *testing.T) *submissionHarness {
	t.Helper()
	store, _ := submissionStore(t)
	h := &submissionHarness{store: store, clock: &submissionTestClock{at: time.Date(2026, 10, 1, 5, 15, 0, 0, time.UTC).UnixMilli()}}
	client := &http.Client{Transport: submissionTransport(func(r *http.Request) (*http.Response, error) {
		h.requests++
		if strings.HasSuffix(r.URL.Path, "/readme") {
			h.readmes++
		}
		if h.response != nil {
			return h.response(r)
		}
		return h.success(r)
	})}
	h.scheduler = NewScheduler(store, SchedulerOptions{Clock: h.clock, Client: client, StartedAt: h.clock.Now(), Token: "test-token"})
	h.worker = NewSubmissionWorker(h.scheduler)
	return h
}
func (h *submissionHarness) success(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/readme") {
		return submissionResponse(200, "a link https://linux.do"), nil
	}
	body, _ := io.ReadAll(r.Body)
	if strings.Contains(string(body), "nodes(ids:") {
		return submissionResponse(200, `{"data":{"nodes":[`+submissionRepository+`]}}`), nil
	}
	return submissionResponse(200, `{"data":{"r0":`+submissionRepository+`}}`), nil
}
func (h *submissionHarness) enqueue(t *testing.T, name string) Submission {
	t.Helper()
	sub, err := h.store.EnqueueSubmission(context.Background(), "https://github.com/"+name, h.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	return sub
}
func (h *submissionHarness) step(t *testing.T) {
	t.Helper()
	worked, err := h.worker.ProcessNext(context.Background())
	if err != nil || !worked {
		t.Fatalf("worked=%t err=%v", worked, err)
	}
}
func (h *submissionHarness) result(t *testing.T, id string) Submission {
	t.Helper()
	sub, err := h.store.Submission(id, h.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

func TestSubmissionAdmissionIndependentStagesAndNoArtificialListing(t *testing.T) {
	h := newSubmissionHarness(t)
	sub := h.enqueue(t, "acme/tool")
	// Startup's ordinary sampling wait must not postpone intake.
	if h.scheduler.startAt <= h.clock.Now() {
		t.Fatal("fixture is not in startup wait")
	}
	h.step(t)
	if got := h.result(t, sub.ID); got.GitHub != "tracked" || got.Linuxdo != "pending" {
		t.Fatal(got)
	}
	github, _ := h.store.ProjectRows("WHERE type='github'")
	if len(github) != 1 || github[0].ListedAt != 0 {
		t.Fatal(github)
	}
	h.step(t)
	if got := h.result(t, sub.ID); got.Status != "completed" || got.Linuxdo != "tracked" {
		t.Fatal(got)
	}
	rows, err := h.store.ProjectRows("ORDER BY type")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ObservedAt != rows[1].ObservedAt || rows[0].Cycle == rows[1].Cycle {
		t.Fatal("did not share one observation across independent cycles", rows)
	}
	for _, row := range rows {
		if row.Homepage == nil || *row.Homepage != "https://example.com/tool" {
			t.Fatal("submission lost homepage", row)
		}
		if row.ListedAt != 0 {
			t.Fatal("submission recorded an artificial listing")
		}
	}
	handler := Handler(h.store, HTTPOptions{Now: func() time.Time { return time.UnixMilli(h.clock.Now()) }})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/projects", nil))
	var list ListResult
	if err = json.Unmarshal(w.Body.Bytes(), &list); err != nil || list.Items[0].ListedAt != nil {
		t.Fatal("not represented as null", w.Body, err)
	}
	original := github[0]
	readmes := h.readmes
	again := h.enqueue(t, "ACME/TOOL")
	h.step(t)
	h.step(t)
	if h.readmes != readmes || h.result(t, again.ID).Status != "completed" {
		t.Fatal("tracked Linux.do repository was rechecked")
	}
	github, _ = h.store.ProjectRows("WHERE type='github'")
	if github[0].Cycle != original.Cycle || github[0].EnteredAt != original.EnteredAt {
		t.Fatal("duplicate reset protection/cycle")
	}
	// Existing discovery writes a real listing through the same observation writer.
	repo, _ := rowRepository(github[0])
	listed := h.clock.Now() + Hour
	if err = h.store.saveObservations(context.Background(), []observation{{repo: repo, previous: &github[0], listedAt: &listed, observedAt: listed}}, listed, nil); err != nil {
		t.Fatal(err)
	}
	github, _ = h.store.ProjectRows("WHERE type='github'")
	if github[0].ListedAt != listed {
		t.Fatal("real discovery did not replace absent listing")
	}
	if _, err = h.store.DB.Exec("DELETE FROM projects WHERE type='github'"); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(listed + Hour)
	reentered := h.enqueue(t, "acme/tool")
	h.step(t)
	h.step(t)
	github, _ = h.store.ProjectRows("WHERE type='github'")
	if github[0].Cycle == original.Cycle || h.result(t, reentered.ID).GitHub != "tracked" {
		t.Fatal("reentry reused old cycle")
	}
}

func TestSubmissionStableIdentityAliasesAndResultExpiry(t *testing.T) {
	h := newSubmissionHarness(t)
	first := h.enqueue(t, "acme/tool")
	h.step(t)
	h.clock.advance(h.clock.Now() + 1)
	alias := h.enqueue(t, "old/name")
	h.step(t) // The earlier Linux.do stage finishes first.
	h.step(t) // Alias's repository stage now sees a finished first task and both tracked types.
	h.step(t)
	if got := h.result(t, alias.ID); got.GitHub != "tracked" || got.Linuxdo != "tracked" {
		t.Fatal(got)
	}
	// Exercise an active stable-ID merge independently of stage scheduling order.
	if _, err := h.store.DB.Exec("UPDATE submissions SET phase='linuxdo',finished_at=NULL,next_at=? WHERE id=?", h.clock.Now()+Hour, first.ID); err != nil {
		t.Fatal(err)
	}
	activeAlias := h.enqueue(t, "another/name")
	h.step(t)
	got := h.result(t, activeAlias.ID)
	if got.ID != activeAlias.ID || got.Name != "acme/tool" || got.Status != "retrying" {
		t.Fatal("returned alias ID no longer resolves", got)
	}
	h.clock.advance(h.clock.Now() + Hour)
	h.step(t)
	finished := h.result(t, first.ID)
	h.clock.advance(*finished.FinishedAt + 7*Day)
	// Even before physical maintenance, expired results are unavailable.
	if _, err := h.store.Submission(activeAlias.ID, h.clock.Now()); err == nil {
		t.Fatal("alias bypassed result expiry")
	}
	if _, err := h.worker.ProcessNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := h.store.DB.QueryRow("SELECT COUNT(*) FROM submissions WHERE id=?", activeAlias.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("expired root did not clean its aliases", err)
	}
}

func TestSubmissionNegativeCacheAndPartialReadmeFailure(t *testing.T) {
	t.Run("negative cache", func(t *testing.T) {
		h := newSubmissionHarness(t)
		h.response = func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/readme") {
				return submissionResponse(200, "no community marker"), nil
			}
			return h.success(r)
		}
		sub := h.enqueue(t, "acme/tool")
		h.step(t)
		h.step(t)
		if got := h.result(t, sub.ID); got.Linuxdo != "ineligible" || got.GitHub != "tracked" {
			t.Fatal(got)
		}
		again := h.enqueue(t, "acme/tool")
		h.step(t)
		h.step(t)
		if h.readmes != 1 || h.result(t, again.ID).Linuxdo != "ineligible" {
			t.Fatal("cache was bypassed")
		}
		h.clock.advance(h.clock.Now() + Day)
		h.response = nil
		fresh := h.enqueue(t, "acme/tool")
		h.step(t)
		h.step(t)
		if h.readmes != 2 || h.result(t, fresh.ID).Linuxdo != "tracked" {
			t.Fatal("new submission did not trigger expired-cache check")
		}
	})
	t.Run("partial after three", func(t *testing.T) {
		h := newSubmissionHarness(t)
		h.response = func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/readme") {
				return submissionResponse(500, "unavailable"), nil
			}
			return h.success(r)
		}
		sub := h.enqueue(t, "acme/tool")
		h.step(t)
		for i := 0; i < 3; i++ {
			h.step(t)
			current := h.result(t, sub.ID)
			if i < 2 {
				if current.Status != "retrying" || current.NextAt < h.clock.Now()+30*60_000 {
					t.Fatal(current)
				}
				h.clock.advance(current.NextAt)
			}
		}
		got := h.result(t, sub.ID)
		if got.Status != "partial" || got.ReadmeAttempts != 3 || got.GitHub != "tracked" || got.Linuxdo != "unconfirmed" {
			t.Fatal(got)
		}
		rows, _ := h.store.ProjectRows("")
		if len(rows) != 1 || rows[0].Type != "github" {
			t.Fatal("README failure changed GitHub admission")
		}
	})
}

func TestSubmissionPrivacyFailuresAndRateLimitRecovery(t *testing.T) {
	for _, kind := range []string{"private", "missing", "auth", "rate"} {
		t.Run(kind, func(t *testing.T) {
			h := newSubmissionHarness(t)
			h.response = func(r *http.Request) (*http.Response, error) {
				switch kind {
				case "private":
					return submissionResponse(200, `{"data":{"r0":`+strings.Replace(submissionRepository, `"isPrivate":false`, `"isPrivate":true`, 1)+`}}`), nil
				case "missing":
					return submissionResponse(200, `{"data":{"r0":null},"errors":[{"type":"NOT_FOUND"}]}`), nil
				case "auth":
					return submissionResponse(401, "unauthorized"), nil
				default:
					r := submissionResponse(429, "limited")
					r.Header.Set("Retry-After", "7200")
					return r, nil
				}
			}
			sub := h.enqueue(t, "acme/tool")
			h.step(t)
			got := h.result(t, sub.ID)
			if kind == "private" || kind == "missing" {
				if got.Status != "failed" || got.GitHub != "failed" {
					t.Fatal(got)
				}
				if len(h.scheduler.upstream.hosts) > 0 && h.scheduler.upstream.blocked("https://api.github.com/graphql") > h.clock.Now() {
					t.Fatal("a nonexistent repository cooled down all GitHub traffic")
				}
			} else if kind == "auth" {
				if got.Status != "retrying" || got.RepositoryAttempts != 1 {
					t.Fatal("authentication was treated as missing", got)
				}
				for i := 0; i < 2; i++ {
					h.clock.advance(got.NextAt)
					h.step(t)
					got = h.result(t, sub.ID)
				}
				if got.Status != "failed" || got.RepositoryAttempts != 3 {
					t.Fatal(got)
				}
			} else {
				if got.RepositoryAttempts != 0 || got.NextAt < h.clock.Now()+2*Hour {
					t.Fatal("rate limit consumed attempt or lost cooldown", got)
				}
				// Simulate process restart, then run the ordinary sampler and intake concurrently.
				h.scheduler = NewScheduler(h.store, h.scheduler.options)
				h.worker = NewSubmissionWorker(h.scheduler)
				var wg sync.WaitGroup
				wg.Add(2)
				go func() {
					defer wg.Done()
					if err := h.scheduler.Tick(context.Background()); err != nil {
						t.Error(err)
					}
				}()
				go func() {
					defer wg.Done()
					if worked, err := h.worker.ProcessNext(context.Background()); err != nil || worked {
						t.Error("deferred task was processed", worked, err)
					}
				}()
				wg.Wait()
				if h.scheduler.upstream.blocked("https://api.github.com/graphql") < got.NextAt {
					t.Fatal("restart/sampling overwrote cooldown")
				}
				h.clock.advance(got.NextAt)
				h.response = nil
				h.step(t)
				h.step(t)
				if h.result(t, sub.ID).Status != "completed" {
					t.Fatal("did not recover after cooldown")
				}
			}
			if kind != "rate" {
				rows, _ := h.store.ProjectRows("")
				if len(rows) != 0 {
					t.Fatal("inaccessible/private project admitted")
				}
			}
		})
	}
}

func TestSubmissionAdmissionCheckpointIsAtomicAndCancellationIsFree(t *testing.T) {
	h := newSubmissionHarness(t)
	sub := h.enqueue(t, "acme/tool")
	_, err := h.store.DB.Exec(`CREATE TRIGGER fail_checkpoint BEFORE UPDATE ON submissions WHEN NEW.github='tracked' BEGIN SELECT RAISE(ABORT,'injected checkpoint failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.worker.ProcessNext(context.Background()); err == nil {
		t.Fatal("checkpoint trigger did not abort")
	}
	rows, _ := h.store.ProjectRows("")
	if len(rows) != 0 || h.result(t, sub.ID).GitHub != "pending" {
		t.Fatal("project/queue checkpoint committed separately")
	}
	var count int
	if err = h.store.DB.QueryRow("SELECT COUNT(*) FROM snapshot_days").Scan(&count); err != nil || count != 0 {
		t.Fatal("orphan history after rollback", err)
	}
	if _, err = h.store.DB.Exec("DROP TRIGGER fail_checkpoint"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.response = func(r *http.Request) (*http.Response, error) { cancel(); return nil, context.Canceled }
	if _, err = h.worker.ProcessNext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if got := h.result(t, sub.ID); got.RepositoryAttempts != 0 {
		t.Fatal("shutdown consumed attempt", got)
	}
	h.response = nil
	h.worker = NewSubmissionWorker(NewScheduler(h.store, h.scheduler.options))
	h.step(t)
	h.step(t)
	if got := h.result(t, sub.ID); got.Status != "completed" {
		t.Fatal("restart did not recover", got)
	}
}

func TestSubmissionListCacheInvalidatesAndCleanupGateRejects(t *testing.T) {
	h := newSubmissionHarness(t)
	var repo repository
	if err := json.Unmarshal([]byte(submissionRepository), &repo); err != nil {
		t.Fatal(err)
	}
	repo.ID = "BASE"
	repo.Name = "base/repo"
	if err := h.store.saveObservations(context.Background(), []observation{{repo: &repo}}, h.clock.Now(), nil); err != nil {
		t.Fatal(err)
	}
	handler := Handler(h.store, HTTPOptions{Submissions: h.worker, Now: func() time.Time { return time.UnixMilli(h.clock.Now()) }})
	read := func() int {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/projects", nil))
		var result ListResult
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Total
	}
	if read() != 1 {
		t.Fatal("fixture")
	}
	h.enqueue(t, "acme/tool")
	h.step(t)
	if read() != 2 {
		t.Fatal("successful admission left old ten-minute list cache")
	}
	// A cleanup marker must gate both new POSTs and the queued consumer.
	if err := h.store.SetState(linuxdoCleanupKey, linuxdoCleanupState{StartedAt: h.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	if paused, err := h.worker.Paused(context.Background()); err != nil || !paused {
		t.Fatal(paused, err)
	}
	calls := h.requests
	if worked, err := h.worker.ProcessNext(context.Background()); err != nil || worked || calls != h.requests {
		t.Fatal("cleanup did not pause intake", worked, err)
	}
}
