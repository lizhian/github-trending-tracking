package tracking_test

import (
	"fmt"
	"github.com/lizhian/github-trending-tracking/internal/tracking"
	"testing"
)

func TestTieredHistoryReturnsWindowPointsAndPermanentlyPrunesUnion(t *testing.T) {
	end := beijing("2026-11-05T14:05:00")
	h := newCollectionHarness(t, end+1)
	seedMatureProjects(t, h, 1, end)
	if _, err := h.store.DB.Exec("DELETE FROM snapshot_days"); err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i <= 720; i++ {
		at := end - i*tracking.Hour
		point := fmt.Sprintf(`{"%d":[%d,%d]}`, at/tracking.Hour, at, 1000-i)
		if _, err := h.store.DB.Exec(`INSERT INTO snapshot_days(cycle,day,hours) VALUES('cycle-R_0',?,?) ON CONFLICT(cycle,day) DO UPDATE SET hours=json_patch(hours,excluded.hours)`, at/tracking.Day, point); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.store.DB.Exec("UPDATE projects SET stars=1000 WHERE id='github:R_0'"); err != nil {
		t.Fatal(err)
	}
	// Maintenance must apply the same point selection used by details.
	h.tick(end + 1)
	for _, tc := range []struct{ width, count int }{{24, 25}, {72, 25}, {168, 29}, {720, 31}} {
		got := requestJSON(t, h.store, h.clock, fmt.Sprintf("/api/projects/github:R_0/history?window=%d", tc.width))
		points := got["points"].([]any)
		trend := got["trend"].(map[string]any)
		if len(points) != tc.count || len(got["gaps"].([]any)) != 0 || trend["state"] != "complete" {
			t.Fatalf("window %d: %v", tc.width, got)
		}
		if points[len(points)-1].([]any)[0] != float64(end) {
			t.Fatal("latest point missing")
		}
	}
	all, err := h.store.Histories([]string{"cycle-R_0"}, end+1)
	if err != nil {
		t.Fatal(err)
	}
	// 25 hourly + 16 additional three-hour + 16 additional six-hour + 23 daily points.
	if len(all["cycle-R_0"]) != 80 {
		t.Fatalf("retained %d points, want 80", len(all["cycle-R_0"]))
	}
	h.tick(end + 2)
	again, _ := h.store.Histories([]string{"cycle-R_0"}, end+2)
	if len(again["cycle-R_0"]) != 80 {
		t.Fatal("pruning not stable")
	}
	// Restart while collection remains blocked; wall clock must not thin the last valid 24 hours.
	h.scheduler = tracking.NewScheduler(h.store, tracking.SchedulerOptions{Clock: h.clock, Client: h.client, StartedAt: end + 2*tracking.Day})
	h.tick(end + 2*tracking.Day)
	got := requestJSON(t, h.store, h.clock, "/api/projects/github:R_0/history?window=24")
	if len(got["points"].([]any)) != 25 {
		t.Fatal("stalled window was thinned", got)
	}
	h.scheduler = tracking.NewScheduler(h.store, tracking.SchedulerOptions{Clock: h.clock, Client: h.client, StartedAt: end + 31*tracking.Day})
	h.tick(end + 31*tracking.Day)
	got = requestJSON(t, h.store, h.clock, "/api/projects/github:R_0/history?window=720")
	if len(got["points"].([]any)) != 1 || got["trend"].(map[string]any)["delta"] != nil {
		t.Fatal("expired latest point lost or comparable", got)
	}
}

func TestWindowGapsIgnoreThinningButKeepMissingSelectedRounds(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-11-05T08:50:00"))
	h.tick(beijing("2026-11-05T09:12:00"))
	h.stars[0] = 120
	h.tick(beijing("2026-11-05T12:12:00"))
	got := requestJSON(t, h.store, h.clock, "/api/projects/github:R_0/history?window=72")
	if len(got["points"].([]any)) != 2 || len(got["gaps"].([]any)) != 0 {
		t.Fatal("planned three-hour interval became gap", got)
	}
	h.stars[0] = 90
	h.tick(beijing("2026-11-05T18:12:00"))
	got = requestJSON(t, h.store, h.clock, "/api/projects/github:R_0/history?window=72")
	trend := got["trend"].(map[string]any)
	if len(got["points"].([]any)) != 3 || len(got["gaps"].([]any)) != 1 || trend["delta"] != float64(-10) {
		t.Fatal("missing 15:00 round or negative delta lost", got)
	}
	item := h.list("github")["items"].([]any)[0].(map[string]any)
	summary := item["trends"].(map[string]any)["72"].(map[string]any)
	if mustJSON(summary["gaps"]) != mustJSON(got["gaps"]) || summary["delta"] != trend["delta"] {
		t.Fatal("list and detail differ")
	}
}

func TestResponsesBoundHistoryToReadProjectObservation(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-11-05T08:50:00"))
	h.tick(beijing("2026-11-05T09:00:00"))
	rows, err := h.store.ProjectRows("WHERE id='github:R_0'")
	if err != nil {
		t.Fatal(err)
	}
	// Model a newer historical read after the project row was already read.
	at := beijing("2026-11-05T12:00:00")
	raw := mustJSON(map[string]any{fmt.Sprint(at / tracking.Hour): [2]int64{at, 200}})
	if _, err := h.store.DB.Exec(`INSERT INTO snapshot_days VALUES(?,?,?) ON CONFLICT(cycle,day) DO UPDATE SET hours=json_patch(hours,excluded.hours)`, rows[0].Cycle, at/tracking.Day, raw); err != nil {
		t.Fatal(err)
	}
	h.clock.at = at
	got := requestJSON(t, h.store, h.clock, "/api/projects/github:R_0/history?window=24")
	if got["trend"].(map[string]any)["end"] != got["observedAt"] {
		t.Fatal("detail mixed observation versions", got)
	}
	item := h.list("github")["items"].([]any)[0].(map[string]any)
	if item["trends"].(map[string]any)["24"].(map[string]any)["end"] != item["observedAt"] {
		t.Fatal("list mixed observation versions", item)
	}
}
