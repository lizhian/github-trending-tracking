package tracking_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/lizhian/github-trending-tracking/internal/tracking"
)

var linuxFeeds = []string{
	"https://linux.do/c/develop/4.rss",
	"https://linux.do/tag/2234-tag/2234.rss",
	"https://linux.do/c/domestic/98.rss",
	"https://linux.do/tag/1451-tag/1451.rss",
	"https://rss.yicai-tech.com/rss/from/https://linux.do/c/develop/4.rss",
	"https://rss.yicai-tech.com/rss/from/https://linux.do/tag/2234-tag/2234.rss",
	"https://rss.yicai-tech.com/rss/from/https://linux.do/c/domestic/98.rss",
}

type linuxSourceState struct {
	AttemptedAt int64   `json:"attemptedAt"`
	SucceededAt *int64  `json:"succeededAt"`
	Succeeded   int     `json:"succeeded"`
	Failed      int     `json:"failed"`
	Error       *string `json:"error"`
	NextAt      int64   `json:"nextAt"`
}

func (h *collectionHarness) linuxSource() linuxSourceState {
	h.t.Helper()
	var state struct {
		Sources map[string]linuxSourceState `json:"sources"`
	}
	if _, err := h.store.State("discoverySources", &state); err != nil {
		h.t.Fatal(err)
	}
	return state.Sources["linuxdo"]
}

func TestLinuxPartialFailurePreservesCandidatesAndRetriesWholeGroup(t *testing.T) {
	for _, failure := range []string{"http", "invalid RSS"} {
		t.Run(failure, func(t *testing.T) {
			h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
			first := beijing("2026-10-01T09:00:00")
			h.tick(first) // All seven empty feeds establish a full success.
			if st := h.linuxSource(); st.Failed != 0 || st.SucceededAt == nil || *st.SucceededAt != first {
				t.Fatal("empty RSS was not successful", st)
			}
			reads := make([]int, len(linuxFeeds))
			failed := true
			for i, feed := range linuxFeeds {
				h.sources[feed] = func() *http.Response {
					reads[i]++
					if i == 1 && failed {
						res := response("private-response-body")
						if failure == "http" {
							res.StatusCode = 503
						}
						return res
					}
					return response(`<rss><channel><item><description>https://github.com/acme/one</description></item></channel></rss>`)
				}
			}
			second := first + tracking.Hour
			h.tick(second)
			st := h.linuxSource()
			status := h.list("linux.do")["status"].(map[string]any)["discovery"].(map[string]any)
			if h.list("linux.do")["total"] != float64(1) || st.Succeeded != 1 || st.Failed != 1 || st.Error == nil || !strings.Contains(*st.Error, "RSS 2:") || strings.Contains(*st.Error, "private") || *st.SucceededAt != first || status["succeededAt"] != float64(first) || status["failed"] != float64(1) {
				t.Fatal("partial results or failure state lost", st, status)
			}
			if st.NextAt < second+5*60_000 {
				t.Fatal("retry scheduled too early", st)
			}
			h.tick(st.NextAt - 1)
			for _, count := range reads {
				if count != 1 {
					t.Fatal("group retried before its deadline", reads)
				}
			}
			failed = false
			h.tick(st.NextAt)
			for _, count := range reads {
				if count != 2 {
					t.Fatal("did not retry the whole group", reads)
				}
			}
			if got := h.linuxSource(); got.Failed != 0 || got.Error != nil || *got.SucceededAt != st.NextAt {
				t.Fatal("full recovery did not clear failure", got)
			}
		})
	}
}

func TestLinuxAllFeedsFailAsOneSource(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	reads := 0
	for _, feed := range linuxFeeds {
		h.sources[feed] = func() *http.Response {
			reads++
			res := response("private-body")
			res.StatusCode = 503
			return res
		}
	}
	h.tick(beijing("2026-10-01T09:00:00"))
	st := h.linuxSource()
	status := h.list("linux.do")["status"].(map[string]any)["discovery"].(map[string]any)
	if reads != 7 || st.Failed != 1 || st.SucceededAt != nil || status["failed"] != float64(1) || !strings.Contains(*st.Error, "RSS 7: RSS HTTP 503") {
		t.Fatal(reads, st, status)
	}
}

func TestLinuxDirectCooldownDoesNotDelayProxyAndPreservesFailure(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	first := beijing("2026-10-01T09:00:00")
	reads := make([]int, len(linuxFeeds))
	for i, feed := range linuxFeeds {
		h.sources[feed] = func() *http.Response {
			reads[i]++
			if i == 0 {
				res := response("limited")
				res.StatusCode = 429
				res.Header.Set("Retry-After", "7200")
				return res
			}
			return response(`<rss><channel><item><description>https://github.com/acme/one</description></item></channel></rss>`)
		}
	}
	h.tick(first)
	st := h.linuxSource()
	if st.Failed != 1 || st.SucceededAt != nil || h.list("linux.do")["total"] != float64(1) || st.NextAt >= first+tracking.Hour {
		t.Fatal("direct cooldown blocked proxy results or retry", st)
	}
	for i, count := range reads {
		want := 1
		if i > 0 && i < 4 {
			want = 0
		}
		if count != want {
			t.Fatal("host cooldown was not respected", reads)
		}
	}
	h.tick(st.NextAt)
	if got := h.linuxSource(); got.Failed != 1 || got.Error == nil || *got.Error != *st.Error || got.SucceededAt != nil || reads[0] != 1 || reads[4] != 2 {
		t.Fatal("cooldown erased a real failure or delayed proxy", got, reads)
	}
}

func TestLinuxBothHostsCoolingWaitsForEarliestHost(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	first := beijing("2026-10-01T09:00:00")
	if err := h.store.SetState("upstream", map[string]any{
		"linux.do":           map[string]any{"blockedUntil": first + 20*60_000},
		"rss.yicai-tech.com": map[string]any{"blockedUntil": first + 40*60_000},
	}); err != nil {
		t.Fatal(err)
	}
	reads := 0
	for _, feed := range linuxFeeds {
		h.sources[feed] = func() *http.Response { reads++; return response(`<rss><channel/></rss>`) }
	}
	h.tick(first)
	st := h.linuxSource()
	if reads != 0 || st.Failed != 0 || st.SucceededAt != nil || st.NextAt != first+20*60_000 {
		t.Fatal("cooldown became a failure or wrong retry", reads, st)
	}
	h.tick(st.NextAt - 1)
	if reads != 0 {
		t.Fatal("queried cooling hosts", reads)
	}
	h.tick(st.NextAt)
	if reads != 4 || h.linuxSource().SucceededAt != nil {
		t.Fatal("earliest host did not resume independently", reads, h.linuxSource())
	}
}

func TestLinuxBudgetExhaustionRetainsCandidatesWithoutFailure(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	first := beijing("2026-10-01T09:00:00")
	reads := 0
	for i, feed := range linuxFeeds {
		h.sources[feed] = func() *http.Response {
			reads++
			if i == 0 {
				h.clock.at += 3 * 60_000
				return response(`<rss><channel><item><description>https://github.com/acme/one</description></item></channel></rss>`)
			}
			return response(`<rss><channel/></rss>`)
		}
	}
	h.tick(first)
	st := h.linuxSource()
	if reads != 1 || st.Failed != 0 || st.SucceededAt != nil || st.NextAt < first+8*60_000 || h.list("linux.do")["total"] != float64(1) {
		t.Fatal("budget discarded candidates or became failure", reads, st, h.list("linux.do"))
	}
	h.sources[linuxFeeds[0]] = func() *http.Response { reads++; return response(`<rss><channel/></rss>`) }
	h.tick(st.NextAt)
	if reads != 8 || h.linuxSource().SucceededAt == nil {
		t.Fatal("budget did not retry the whole group", reads, h.linuxSource())
	}
}
