package tracking_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/lizhian/github-trending-tracking/internal/tracking"
)

func TestLanguageTrendingSubmitsPartialRankingAndResumesAfterRestart(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	h.sources["https://rsshub-balancer.virworks.moe/github/trending/daily/any"] = func() *http.Response {
		return response(`<rss><channel/></rss>`)
	}
	h.names = []string{"acme/new"}
	h.stars = []int64{200}
	var table strings.Builder
	row := func(name, count string) string {
		return fmt.Sprintf(`<article class="Box-row"><h2><a href="/%s">%s</a></h2><span class="float-sm-right"><svg></svg>%s</span></article>`, name, name, count)
	}
	for i := range 101 {
		name := fmt.Sprintf("acme/repo-%03d", i)
		h.names = append(h.names, name)
		h.stars = append(h.stars, 100)
		table.WriteString(row(name, "1 star today"))
	}
	malformed := true
	calls := map[string]int{}
	base := h.client.Transport
	h.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "github.com" || !strings.HasPrefix(r.URL.Path, "/trending/") {
			return base.RoundTrip(r)
		}
		if r.URL.Query().Get("since") != "daily" || r.Header.Get("Authorization") != "" {
			t.Fatal(r.URL)
		}
		calls[r.URL.Path]++
		switch r.URL.Path {
		case "/trending/c++":
			return response(table.String()), nil
		case "/trending/go":
			return response(row("ACME/REPO-099", "1,001 stars today")), nil
		case "/trending/c#":
			if !strings.HasSuffix(r.URL.EscapedPath(), "c%23") {
				t.Fatal(r.URL)
			}
			if malformed {
				return response(row("acme/new", "5 stars this week")), nil
			}
			return response(row("acme/new", "2,000 stars today")), nil
		}
		return base.RoundTrip(r)
	})
	find := func() map[string]any { return h.source("github", "github-language-daily") }
	readNames := func() map[string]bool {
		t.Helper()
		names := map[string]bool{}
		rows, err := h.store.ProjectRows("WHERE type='github'")
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			names[strings.ToLower(row.Name)] = true
		}
		var pending []struct{ Name string }
		if _, err := h.store.State("pending", &pending); err != nil {
			t.Fatal(err)
		}
		for _, entry := range pending {
			names[strings.ToLower(entry.Name)] = true
		}
		return names
	}
	first := beijing("2026-10-01T09:00:00")
	h.tick(first)
	status := find()
	firstAdmitted := status["admitted"]
	if firstAdmitted != float64(20) {
		t.Fatal("expected first batch of projects from successful languages", status)
	}
	if len(calls) != 24 || status["languagesCompleted"] != float64(23) || status["succeeded"] != float64(101) || status["succeededAt"] != nil || !strings.Contains(fmt.Sprint(status["error"]), "C#") {
		t.Fatal(calls, status)
	}
	names := readNames()
	if len(names) != 100 || !names["acme/repo-099"] || names["acme/repo-100"] || names["acme/new"] {
		t.Fatal("expected the top 100 from successful languages", len(names), names["acme/repo-100"], names["acme/new"])
	}
	malformed = false
	h.scheduler = tracking.NewScheduler(h.store, tracking.SchedulerOptions{
		Clock: h.clock, Client: h.client, StartedAt: first + 4*60_000, Token: "test-token",
	})
	h.tick(beijing("2026-10-01T10:00:00"))
	status, names = find(), readNames()
	if status["admitted"] == firstAdmitted || status["admitted"] != h.list("github")["total"] {
		t.Fatal("same-slot retry did not accumulate admissions", status)
	}
	if calls["/trending/c++"] != 1 || calls["/trending/c#"] != 2 || status["languagesCompleted"] != float64(24) || status["failed"] != float64(0) || status["succeededAt"] == nil {
		t.Fatal(calls, status)
	}
	if len(names) != 101 || !names["acme/new"] || names["acme/repo-100"] {
		t.Fatal("retry should only add the newly ranked candidate", len(names), names["acme/new"])
	}
	h.tick(beijing("2026-10-01T20:59:00"))
	if calls["/trending/c++"] != 1 {
		t.Fatal("language pages fetched before the evening slot", calls)
	}
	h.tick(beijing("2026-10-01T21:00:00"))
	if calls["/trending/c++"] != 2 || calls["/trending/c#"] != 3 {
		t.Fatal("evening slot did not start a new round", calls)
	}
	if status := find(); status["admitted"] != float64(0) {
		t.Fatal("previous slot's pending candidates counted toward the evening slot", status)
	}
}
