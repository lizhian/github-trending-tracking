package tracking

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestCurrentWindowsDecayWithoutCreatingObservations(t *testing.T) {
	end := int64(1800000000000)
	points := []Point{{end - 20*Day, 100}, {end - Day, 200}, {end, 300}}
	row := ProjectRow{ObservedAt: end, Stars: 300, UnavailableAt: end + Hour, CarryPeriods: "[]"}
	for _, width := range Windows {
		got := ProjectTrend(row, points, width, end+10*Day, nil)
		want := int64(0)
		if width == 720 {
			want = 200
		}
		if got.Delta == nil || *got.Delta != want || got.Basis != "carried" {
			t.Fatalf("width=%d: %+v", width, got)
		}
		for _, p := range got.Points {
			if p[0] > end {
				t.Fatal("invented observation", p)
			}
		}
	}
	got := ProjectTrend(row, points, 720, end+40*Day, nil)
	if got.Delta == nil || *got.Delta != 0 {
		t.Fatal("old history must decay to zero", got)
	}
	row.UnavailableAt = 0
	got = ProjectTrend(row, points, 24, end+10*Day, nil)
	if got.Delta != nil || got.Basis != "unknown" {
		t.Fatal("temporary failure must remain unknown", got)
	}
	// A recovery after a long outage attributes its difference to the recovery window.
	row.ObservedAt = end + 40*Day
	row.Stars = 350
	row.CarryPeriods = jsonText([]carryPeriod{{Start: end, End: row.ObservedAt, Stars: 300}})
	got = ProjectTrend(row, []Point{{row.ObservedAt, 350}}, 24, row.ObservedAt+1, nil)
	if got.Delta == nil || *got.Delta != 50 || len(got.Points) != 1 {
		t.Fatal("recovery delta lost", got)
	}
	got = ProjectTrend(row, []Point{{row.ObservedAt, 350}, {row.ObservedAt + 2*Day, 350}}, 24, row.ObservedAt+2*Day, nil)
	if got.Delta == nil || *got.Delta != 0 {
		t.Fatal("recovery increase did not age out", got)
	}
}

func TestTrackedUnavailableConfirmationAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, graphql string
		status        int
		confirmed     bool
	}{
		{"confirmed", `{"data":{"nodes":[null]},"errors":[{"type":"NOT_FOUND","path":["nodes",0]}]}`, 404, true},
		{"ambiguous", `{"data":{"nodes":[null]}}`, 404, false},
		{"temporary", `{"data":{"nodes":[null]},"errors":[{"type":"NOT_FOUND","path":["nodes",0]}]}`, 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newSubmissionHarness(t)
			at := h.clock.Now()
			var repo repository
			if err := json.Unmarshal([]byte(submissionRepository), &repo); err != nil {
				t.Fatal(err)
			}
			for _, typ := range projectTypes {
				if err := h.store.saveObservations(context.Background(), []observation{{repo: &repo, typ: typ}}, at-Day, nil); err != nil {
					t.Fatal(err)
				}
			}
			var before int
			_ = h.store.DB.QueryRow("SELECT COUNT(*) FROM snapshot_days").Scan(&before)
			h.scheduler.startAt = at
			h.response = func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/graphql" {
					return submissionResponse(200, tc.graphql), nil
				}
				if r.URL.Host == "api.github.com" {
					return submissionResponse(tc.status, ""), nil
				}
				return submissionResponse(200, `<rss><channel/></rss>`), nil
			}
			if err := h.scheduler.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			rows, err := h.store.ProjectRows("")
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if (row.UnavailableAt > 0) != tc.confirmed || row.ObservedAt != at-Day || row.Stars != 100 {
					t.Fatalf("incorrect state: %+v", row)
				}
			}
			var after int
			_ = h.store.DB.QueryRow("SELECT COUNT(*) FROM snapshot_days").Scan(&after)
			if before != after {
				t.Fatal("failure wrote observations")
			}
			if !tc.confirmed {
				return
			}
			h.clock.advance(at + Hour)
			h.response = func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/graphql" {
					return submissionResponse(200, `{"data":{"nodes":[`+submissionRepository+`]}}`), nil
				}
				if r.URL.Host == "api.github.com" {
					return submissionResponse(200, `https://linux.do`), nil
				}
				return submissionResponse(200, `<rss><channel/></rss>`), nil
			}
			if err := h.scheduler.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			rows, err = h.store.ProjectRows("")
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				var periods []carryPeriod
				if err := json.Unmarshal([]byte(row.CarryPeriods), &periods); err != nil {
					t.Fatal(err)
				}
				if row.UnavailableAt != 0 || row.ObservedAt != at+Hour || len(periods) != 1 {
					t.Fatalf("did not recover: %+v", row)
				}
			}
		})
	}
}
