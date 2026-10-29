package tracking

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestUnavailableCandidateRequiresExplicitMissingAndAnonymous404(t *testing.T) {
	for _, tc := range []struct {
		name, graphql string
		status        int
		removed       bool
	}{
		{"confirmed", `{"data":{"r0":null},"errors":[{"type":"NOT_FOUND","path":["r0"]}]}`, 404, true},
		{"ambiguous-null", `{"data":{"r0":null}}`, 404, false},
		{"permission-error", `{"data":{"r0":null},"errors":[{"type":"FORBIDDEN","path":["r0"]}]}`, 404, false},
		{"anonymous-forbidden", `{"data":{"r0":null},"errors":[{"type":"NOT_FOUND","path":["r0"]}]}`, 403, false},
		{"anonymous-unavailable", `{"data":{"r0":null},"errors":[{"type":"NOT_FOUND","path":["r0"]}]}`, 503, false},
		{"anonymous-rate-limit", `{"data":{"r0":null},"errors":[{"type":"NOT_FOUND","path":["r0"]}]}`, 429, false},
		{"anonymous-timeout", `{"data":{"r0":null},"errors":[{"type":"NOT_FOUND","path":["r0"]}]}`, 0, false},
		{"anonymous-malformed", `{"data":{"r0":null},"errors":[{"type":"NOT_FOUND","path":["r0"]}]}`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newSubmissionHarness(t)
			h.scheduler.startAt = h.clock.Now()
			anonymous := 0
			h.response = func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.github.com" {
					return submissionResponse(200, `<rss><channel/></rss>`), nil
				}
				if r.URL.Path == "/graphql" {
					return submissionResponse(200, tc.graphql), nil
				}
				if r.Header.Get("Authorization") != "" {
					t.Fatal("anonymous confirmation carried a token")
				}
				anonymous++
				if tc.status == 0 {
					return nil, errors.New("timeout")
				}
				return submissionResponse(tc.status, ""), nil
			}
			if err := h.store.SetState("pending", []*discoveryEntry{{Type: "linux.do", Name: "acme/gone"}}); err != nil {
				t.Fatal(err)
			}
			if err := h.scheduler.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			var pending []*discoveryEntry
			_, _ = h.store.State("pending", &pending)
			if (len(pending) == 0) != tc.removed {
				t.Fatalf("pending=%+v removed=%v", pending, tc.removed)
			}
			var cached struct{ CheckedAt, ExpiresAt int64 }
			found, err := h.store.State("repository_unavailable:acme/gone", &cached)
			if err != nil || found != tc.removed {
				t.Fatalf("cache found=%v err=%v", found, err)
			}
			if tc.removed && (anonymous != 1 || cached.ExpiresAt != cached.CheckedAt+Day) {
				t.Fatal(anonymous, cached)
			}
			var admission runState
			_, _ = h.store.State("admission:linux.do", &admission)
			if (admission.Failed == 0) != tc.removed {
				t.Fatal(admission)
			}
		})
	}
}

func TestSubmissionClearsUnavailableCacheIncludingAlias(t *testing.T) {
	for _, alias := range []bool{false, true} {
		t.Run(map[bool]string{false: "admit", true: "alias"}[alias], func(t *testing.T) {
			h := newSubmissionHarness(t)
			if err := h.store.SetState(readmeCacheKey("R1"), readmeResult{Reason: "missing-marker", CheckedAt: h.clock.Now(), ExpiresAt: h.clock.Now() + Day}); err != nil {
				t.Fatal(err)
			}
			if alias {
				root := h.enqueue(t, "acme/tool")
				h.step(t)
				if _, err := h.store.DB.Exec("UPDATE submissions SET next_at=? WHERE id=?", h.clock.Now()+Hour, root.ID); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"acme/old", "acme/tool"} {
				if err := h.store.SetState("repository_unavailable:"+name, map[string]int64{"checkedAt": h.clock.Now(), "expiresAt": h.clock.Now() + Day}); err != nil {
					t.Fatal(err)
				}
			}
			sub := h.enqueue(t, "acme/old")
			h.step(t)
			if alias {
				var phase string
				_ = h.store.DB.QueryRow("SELECT phase FROM submissions WHERE id=?", sub.ID).Scan(&phase)
				if phase != "alias" {
					t.Fatal(phase)
				}
			}
			for _, name := range []string{"acme/old", "acme/tool"} {
				var cached any
				if found, err := h.store.State("repository_unavailable:"+name, &cached); err != nil || found {
					t.Fatal(name, found, err)
				}
			}
			if got := h.result(t, sub.ID); got.GitHub != "tracked" && !strings.Contains(got.Phase, "alias") {
				t.Fatal(got)
			}
			if alias {
				h.clock.advance(h.clock.Now() + Hour)
			}
			h.step(t)
			if got := h.result(t, sub.ID); got.Linuxdo != "ineligible" || h.readmes != 0 {
				t.Fatal("README cache bypassed", got, h.readmes)
			}
		})
	}
}

// Keep discovery empty so tests exercise the persisted candidate path without
// unrelated trend-source fixtures or live network calls.
func candidateResponse(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "api.github.com" {
		return submissionResponse(200, `<rss><channel/></rss>`), nil
	}
	if r.URL.Path == "/graphql" {
		return submissionResponse(200, `{"data":{"r0":null},"errors":[{"type":"NOT_FOUND","path":["r0"]}]}`), nil
	}
	return submissionResponse(404, ""), nil
}

func TestLookupMissingIsTargetSpecific(t *testing.T) {
	h := newSubmissionHarness(t)
	h.response = func(r *http.Request) (*http.Response, error) {
		return submissionResponse(200, `{"data":{"r0":null,"r1":null},"errors":[{"type":"NOT_FOUND","path":["r0"]},{"type":"FORBIDDEN","path":["r1"]}]}`), nil
	}
	_, missing, err := h.scheduler.upstream.lookupRepositories(context.Background(), "test-token", []string{"acme/gone", "acme/blocked"}, false, "identity")
	if err != nil || !reflect.DeepEqual(missing, []bool{true, false}) {
		t.Fatal(missing, err)
	}
}

func TestUnavailableCacheSurvivesRestartAndRequiresRediscovery(t *testing.T) {
	h := newSubmissionHarness(t)
	h.scheduler.startAt = h.clock.Now()
	anonymous := 0
	h.response = func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			anonymous++
		}
		return candidateResponse(r)
	}
	setPending := func(entries ...*discoveryEntry) {
		t.Helper()
		if err := h.store.SetState("pending", entries); err != nil {
			t.Fatal(err)
		}
	}
	tick := func() {
		t.Helper()
		if err := h.scheduler.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	setPending(&discoveryEntry{Type: "linux.do", Name: "Acme/Gone"}, &discoveryEntry{Type: "github", Name: "acme/gone"})
	tick()
	var cached unavailableResult
	if found, err := h.store.State(unavailableCacheKey("acme/gone"), &cached); err != nil || !found || anonymous != 1 {
		t.Fatal(found, err, anonymous)
	}
	h.scheduler = NewScheduler(h.store, h.scheduler.options)
	h.scheduler.startAt = h.clock.Now()
	setPending(&discoveryEntry{Type: "github", Name: "ACME/gone", NextAt: h.clock.Now() + Hour})
	tick()
	var after unavailableResult
	_, _ = h.store.State(unavailableCacheKey("acme/gone"), &after)
	var pending []*discoveryEntry
	_, _ = h.store.State("pending", &pending)
	if after != cached || anonymous != 1 || len(pending) != 0 {
		t.Fatal(after, anonymous, pending)
	}
	h.clock.advance(cached.ExpiresAt)
	tick()
	if found, err := h.store.State(unavailableCacheKey("acme/gone"), &after); err != nil || found || anonymous != 1 {
		t.Fatal(found, err, anonymous)
	}
	// A later source discovery queues a fresh candidate; expiration alone did not.
	h.response = func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			anonymous++
		}
		if r.URL.Host == "linux.do" {
			return submissionResponse(200, `<rss><channel><item><description>https://github.com/acme/gone</description></item></channel></rss>`), nil
		}
		return candidateResponse(r)
	}
	h.clock.advance(h.clock.Now() + Hour)
	tick()
	if anonymous != 2 {
		t.Fatal("rediscovery did not recheck", anonymous)
	}
}

func TestUnavailableCandidateRetainsOtherFailuresAndClearsCachedList(t *testing.T) {
	h := newSubmissionHarness(t)
	// Populate a tracked project so HTTP list response caching is active.
	h.enqueue(t, "acme/tool")
	h.step(t)
	h.step(t)
	rows, err := h.store.ProjectRows("")
	if err != nil {
		t.Fatal(err)
	}
	h.scheduler.startAt = h.clock.Now()
	future := h.clock.Now() + Hour
	entries := []*discoveryEntry{
		{Type: "linux.do", Name: "acme/gone"},
		{Type: "linux.do", Name: "acme/deferred", NextAt: future, Failure: "GitHub HTTP 503"},
		{Type: "github", Name: "acme/legacy", NextAt: future},
	}
	if err := h.store.SetState("pending", entries); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetState("admission:linux.do", runState{Failed: 2, Error: strptr("old failure")}); err != nil {
		t.Fatal(err)
	}
	handler := Handler(h.store, HTTPOptions{Now: func() time.Time { return time.UnixMilli(h.clock.Now()) }})
	read := func() runState {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/projects?type=linux.do", nil))
		var result struct {
			Status map[string]runState `json:"status"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Status["admission"]
	}
	if read().Failed != 2 {
		t.Fatal("missing fixture state")
	}
	h.response = candidateResponse
	if err := h.scheduler.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := read(); got.Failed != 1 || got.Error == nil || *got.Error != "GitHub HTTP 503" {
		t.Fatal(got)
	}
	var global, github runState
	_, _ = h.store.State("admission", &global)
	_, _ = h.store.State("admission:github", &github)
	if global.Failed != 2 || github.Failed != 1 {
		t.Fatal(global, github)
	}
	// An idle scan must retain failures without inventing a new attempt time.
	h.clock.advance(h.clock.Now() + 60_000)
	if err := h.scheduler.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var idle runState
	_, _ = h.store.State("admission", &idle)
	if !reflect.DeepEqual(idle, global) {
		t.Fatal("idle scan rewrote admission attempt", idle, global)
	}
	after, err := h.store.ProjectRows("")
	if err != nil || !reflect.DeepEqual(after, rows) {
		t.Fatal("tracked rows changed", err)
	}
}

func TestUnavailableCandidateCheckpointRollsBack(t *testing.T) {
	h := newSubmissionHarness(t)
	h.scheduler.startAt = h.clock.Now()
	h.response = candidateResponse
	// Settle discovery first, then fail the final admission-state checkpoint.
	if err := h.scheduler.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	entry := &discoveryEntry{Type: "linux.do", Name: "acme/gone", Failure: "GitHub repository unavailable"}
	if err := h.store.SetState("pending", []*discoveryEntry{entry}); err != nil {
		t.Fatal(err)
	}
	previous := runState{Failed: 1, Error: strptr("old failure")}
	if err := h.store.SetState("admission:linux.do", previous); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.DB.Exec(`CREATE TRIGGER fail_admission BEFORE UPDATE ON system_state WHEN NEW.key='admission:linux.do' BEGIN SELECT RAISE(ABORT,'injected checkpoint failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := h.scheduler.Tick(context.Background()); err == nil {
		t.Fatal("expected transaction failure")
	}
	var pending []*discoveryEntry
	_, _ = h.store.State("pending", &pending)
	var cached unavailableResult
	found, err := h.store.State(unavailableCacheKey(entry.Name), &cached)
	var admission runState
	_, _ = h.store.State("admission:linux.do", &admission)
	if err != nil || found || len(pending) != 1 || !reflect.DeepEqual(admission, previous) {
		t.Fatal(found, err, pending, admission)
	}
}

func TestUnavailableRepositoryFallbackPreservesIdentity(t *testing.T) {
	for _, stage := range []string{"identity", "readme", "metadata"} {
		for _, anonymous := range []string{"missing", "same", "reused"} {
			t.Run(stage+"/"+anonymous, func(t *testing.T) {
				h := newSubmissionHarness(t)
				h.scheduler.startAt = h.clock.Now()
				entry := &discoveryEntry{Type: "github", Name: "acme/tool", RepoID: "R1"}
				if stage == "identity" {
					entry.RepoID = ""
				}
				if stage == "readme" {
					entry.Type = "linux.do"
				}
				if err := h.store.SetState("pending", []*discoveryEntry{entry}); err != nil {
					t.Fatal(err)
				}
				h.response = func(r *http.Request) (*http.Response, error) {
					if r.URL.Host != "api.github.com" {
						return candidateResponse(r)
					}
					if strings.HasSuffix(r.URL.Path, "/readme") {
						return h.success(r)
					}
					if r.URL.Path == "/graphql" {
						body, _ := io.ReadAll(r.Body)
						if strings.Contains(string(body), "nodes(ids:") {
							return submissionResponse(200, `{"data":{"nodes":[null]},"errors":[{"type":"NOT_FOUND","path":["nodes",0]}]}`), nil
						}
						return candidateResponse(r)
					}
					if anonymous == "missing" {
						return submissionResponse(404, ""), nil
					}
					id := "R1"
					if anonymous == "reused" {
						id = "R_OTHER"
					}
					return submissionResponse(200, `{"node_id":"`+id+`","full_name":"acme/tool","private":false,"stargazers_count":123,"topics":[],"language":"Go"}`), nil
				}
				if err := h.scheduler.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
				var pending []*discoveryEntry
				_, _ = h.store.State("pending", &pending)
				var cached unavailableResult
				found, err := h.store.State(unavailableCacheKey(entry.Name), &cached)
				if err != nil || found != (anonymous == "missing") {
					t.Fatal(found, err)
				}
				rows, err := h.store.ProjectRows("")
				if err != nil {
					t.Fatal(err)
				}
				if anonymous == "missing" {
					if len(pending) != 0 || len(rows) != 0 {
						t.Fatal(pending, rows)
					}
				} else if anonymous == "same" || stage == "identity" {
					if len(rows) != 1 || len(pending) != 0 {
						t.Fatal(rows, pending)
					}
				} else if len(rows) != 0 || len(pending) != 1 {
					t.Fatal("identity mismatch admitted or dropped", rows, pending)
				}
			})
		}
	}
}
