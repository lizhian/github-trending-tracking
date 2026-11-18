package tracking_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// 综合搜索无语言过滤；不完整结果可收录，成功后只在下一个白天时段执行。
func TestGlobalSearchDiscovery(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	initial := h.list("github")["sources"].([]any)
	if len(initial) != 10 {
		t.Fatal(initial)
	}
	h.sources["https://rsshub-balancer.virworks.moe/github/trending/daily/any"] = func() *http.Response { return response(`<rss><channel/></rss>`) }
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
		if q.Get("per_page") != "100" || q.Get("sort") != "stars" || q.Get("order") != "desc" || strings.Contains(q.Get("q"), "language:") {
			t.Fatal(q)
		}
		if partial {
			return response(`{"items":[{"full_name":"acme/one"}],"incomplete_results":true}`), nil
		}
		return response(`{"items":[],"incomplete_results":false}`), nil
	})
	first := beijing("2026-10-01T09:00:00")
	h.tick(first)
	got := h.list("github")
	if got["total"] != float64(1) || searchCalls != 1 {
		t.Fatal("discovery missing", got["total"], searchCalls)
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
	search := find("github-search")
	if search["failed"] != float64(1) || search["succeeded"] != float64(1) || search["succeededAt"] != nil || search["error"] != "GitHub Search incomplete results" {
		t.Fatal(search)
	}
	partial = false
	h.tick(first + 6*60_000)
	search = find("github-search")
	if searchCalls != 2 || search["failed"] != float64(0) || search["succeededAt"] == nil || search["succeeded"] != float64(0) {
		t.Fatal("partial result did not recover", searchCalls, search)
	}
	h.tick(beijing("2026-10-01T21:00:00"))
	if searchCalls != 2 {
		t.Fatal("daily source fetched again", searchCalls)
	}
	h.tick(beijing("2026-10-02T09:00:00"))
	if searchCalls != 3 {
		t.Fatal("next day not fetched", searchCalls)
	}
	linux := h.list("linux.do")["sources"].([]any)
	if len(linux) != 1 || linux[0].(map[string]any)["id"] != "linuxdo" {
		t.Fatal("sources crossed tracking types", linux)
	}
}
