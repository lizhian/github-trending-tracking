package tracking_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// 验证真实调度、候选准入与公开API；部分搜索结果可收录，每日来源不跟随小时采样重复抓取。
func TestLanguageDiscoveryAndSourceDiagnostics(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	initial := h.list("github")["sources"].([]any)
	if len(initial) != 22 || initial[8].(map[string]any)["attemptedAt"] != float64(0) {
		t.Fatal(initial)
	}
	h.sources["https://rsshub-balancer.virworks.moe/github/trending/daily/any"] = func() *http.Response { return response(`<rss><channel/></rss>`) }
	dailyCalls := 0
	h.sources["https://rsshub-balancer.virworks.moe/github/trending/daily/go"] = func() *http.Response {
		dailyCalls++
		return response(`<rss><channel><item><link>https://github.com/acme/two</link></item></channel></rss>`)
	}
	searchCalls := 0
	partial := true
	base := h.client.Transport
	h.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/search/repositories" {
			return base.RoundTrip(r)
		}
		searchCalls++
		if r.Header.Get("Authorization") == "" {
			t.Fatal("Search did not reuse token")
		}
		q := r.URL.Query()
		for _, term := range []string{"stars:>=100", "fork:false", "archived:false", "is:public", "created:>="} {
			if !strings.Contains(q.Get("q"), term) {
				t.Fatal(q)
			}
		}
		for _, term := range strings.Fields(q.Get("q")) {
			if strings.HasPrefix(term, "created:>=") {
				since, err := time.Parse(time.RFC3339, strings.TrimPrefix(term, "created:>="))
				age := h.clock.Now() - since.UnixMilli()
				if err != nil || age < 90*24*3_600_000 || age > 90*24*3_600_000+1000 {
					t.Fatal("wrong rolling window", term, age)
				}
			}
		}
		if q.Get("per_page") != "30" || q.Get("sort") != "stars" || q.Get("order") != "desc" {
			t.Fatal(q)
		}
		if strings.Contains(q.Get("q"), "language:C") && !strings.Contains(q.Get("q"), "language:C++ ") {
			t.Fatal("C++ was not encoded correctly", q)
		}
		if strings.HasPrefix(q.Get("q"), "language:Python ") {
			if partial {
				return response(`{"items":[{"full_name":"acme/one"}],"incomplete_results":true}`), nil
			}
			return response(`{"items":[{"full_name":"acme/one"}],"incomplete_results":false}`), nil
		}
		return response(`{"items":[],"incomplete_results":false}`), nil
	})
	first := beijing("2026-10-01T09:00:00")
	h.tick(first)
	got := h.list("github")
	if got["total"] != float64(2) || searchCalls != 7 || dailyCalls != 1 {
		t.Fatal("discovery missing", got["total"], searchCalls, dailyCalls)
	}
	find := func(id string) map[string]any {
		t.Helper()
		for _, item := range h.list("github")["sources"].([]any) {
			source := item.(map[string]any)
			if source["id"] == id {
				return source
			}
		}
		t.Fatal("missing source", id)
		return nil
	}
	python := find("github-search-python")
	if python["failed"] != float64(1) || python["succeeded"] != float64(1) || python["succeededAt"] != nil || python["error"] != "GitHub Search incomplete results" {
		t.Fatal(python)
	}
	zero := find("github-search-go")
	if zero["failed"] != float64(0) || zero["succeeded"] != float64(0) || zero["succeededAt"] == nil {
		t.Fatal("zero results reported as failure", zero)
	}
	partial = false
	h.tick(first + 6*60_000)
	python = find("github-search-python")
	if searchCalls != 8 || python["failed"] != float64(0) || python["succeededAt"] == nil {
		t.Fatal("partial result did not recover", searchCalls, python)
	}
	h.tick(beijing("2026-10-01T11:00:00"))
	if searchCalls != 8 || dailyCalls != 1 {
		t.Fatal("daily sources fetched again", searchCalls, dailyCalls)
	}
	h.tick(beijing("2026-10-02T09:00:00"))
	if searchCalls != 15 || dailyCalls != 2 {
		t.Fatal("next day not fetched", searchCalls, dailyCalls)
	}
	linux := h.list("linux.do")["sources"].([]any)
	if len(linux) != 1 || linux[0].(map[string]any)["id"] != "linuxdo" {
		t.Fatal("sources crossed tracking types", linux)
	}
}
