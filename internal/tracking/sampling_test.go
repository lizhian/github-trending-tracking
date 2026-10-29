package tracking_test

// 本文件验证采样时间表、历史规则兼容和重试边界，复用 scheduler_test.go 的模拟时钟。
// 不等待真实时间；修改时钟后执行单轮 Tick，能精确检查截止前一毫秒与到期时的行为。

import (
	"net/http"
	"testing"

	"github.com/lizhian/github-trending-tracking/internal/tracking"
)

// TestDaytimeHourlyScheduleAcrossMidnight 依次验证北京时间十八个应采整点，以及跨午夜的最近/下一时段计算。
func TestDaytimeHourlyScheduleAcrossMidnight(t *testing.T) {
	previous := beijing("2026-10-01T23:00:00")
	for _, hour := range []string{"01", "03", "05", "07", "09", "10", "11", "12", "13", "14", "15", "16", "17", "18", "19", "20", "21", "23"} {
		want := beijing("2026-10-02T" + hour + ":00:00")
		if got := tracking.NextSlot(previous); got != want {
			t.Fatalf("next after %d: got %d, want %d", previous, got, want)
		}
		if got := tracking.LatestSlot(want + 59*60_000); got != want {
			t.Fatalf("latest at %s: got %d, want %d", hour, got, want)
		}
		previous = want
	}
	if got := tracking.LatestSlot(beijing("2026-10-03T00:59:59")); got != previous {
		t.Fatal("lost previous day's 23:00 observation", got)
	}
	if got := tracking.NextSlot(previous); got != beijing("2026-10-03T01:00:00") {
		t.Fatal("wrong next-day slot", got)
	}
}

// TestSamplingTransitionPreservesHistoricalGapsAndWindowBoundaries 跨旧/新规则生效边界比较缺测、窗口完整性和过期，确认新计划不重新解释旧历史。
func TestSamplingTransitionPreservesHistoricalGapsAndWindowBoundaries(t *testing.T) {
	p := &tracking.SamplingPolicy{Version: 3, EffectiveAt: beijing("2026-09-01T09:00:00"), HourlyDaytimeAt: beijing("2026-10-02T12:00:00")}
	for _, test := range []struct {
		from, to string
		gap      bool
	}{
		{"2026-10-01T01:00:00", "2026-10-01T05:00:00", false},
		{"2026-10-01T09:00:00", "2026-10-01T11:00:00", false},
		{"2026-10-02T11:00:00", "2026-10-02T12:00:00", false},
		{"2026-10-02T11:00:00", "2026-10-02T13:00:00", true},
		{"2026-10-02T13:00:00", "2026-10-02T15:00:00", true},
		{"2026-10-02T23:00:00", "2026-10-03T01:00:00", false},
	} {
		if got := tracking.MissedSampling(beijing(test.from), beijing(test.to), p); got != test.gap {
			t.Errorf("gap %s -> %s: got %t", test.from, test.to, got)
		}
	}
	// 即使窗口终点已过升级边界，只要左边界仍在旧规则内，
	// 就按旧规则判断其首个应采时段（14:00 之后是 15:00）。
	if !tracking.CompleteWindow(beijing("2026-10-01T15:09:00"), beijing("2026-10-02T14:00:00"), 24, p) {
		t.Fatal("old window boundary was reinterpreted")
	}
	if tracking.CompleteWindow(beijing("2026-10-03T15:09:00"), beijing("2026-10-04T14:00:00"), 24, p) {
		t.Fatal("new window still allowed a missed hourly slot")
	}
	observed := beijing("2026-10-02T11:00:00")
	if tracking.IsStale(observed, beijing("2026-10-02T12:10:00"), p) || !tracking.IsStale(observed, beijing("2026-10-02T12:10:01"), p) {
		t.Fatal("transition did not use the next hourly slot and ten-minute grace")
	}
	if !tracking.IsStale(beijing("2026-10-02T09:00:00"), beijing("2026-10-02T12:00:00"), p) {
		t.Fatal("transition revived an already stale observation")
	}
}

// TestScheduleUpgradeReplansBothSourcesAndPreservesCooldowns 构造旧计划与已持久化冷却，验证来源改用新时刻表、启动门禁仍有效且已有冷却不缩短。
func TestScheduleUpgradeReplansBothSourcesAndPreservesCooldowns(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-02T11:10:00"))
	effective := beijing("2026-09-01T09:00:00")
	first := beijing("2026-10-02T12:00:00")
	if err := h.store.SetState("sampling_policy", tracking.SamplingPolicy{Version: 2, EffectiveAt: effective}); err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"scheduleVersion": 2, "sources": map[string]any{}}
	for _, id := range []string{"github-daily", "linuxdo"} {
		state["sources"].(map[string]any)[id] = map[string]any{"succeededAt": first - tracking.Hour, "nextAt": first + tracking.Hour}
	}
	if err := h.store.SetState("discoverySources", state); err != nil {
		t.Fatal(err)
	}
	until := first + 30*60_000
	if err := h.store.SetState("upstream", map[string]any{
		"api.github.com": map[string]any{"blockedUntil": until},
		"linux.do":       map[string]any{"blockedUntil": first + 20*60_000},
	}); err != nil {
		t.Fatal(err)
	}
	daily, direct, proxy := 0, 0, 0
	h.sources["https://rsshub-balancer.virworks.moe/github/trending/daily/any"] = func() *http.Response { daily++; return response(`<rss><channel/></rss>`) }
	h.sources["https://linux.do/c/develop/4.rss"] = func() *http.Response { direct++; return response(`<rss><channel/></rss>`) }
	h.sources["https://rss.yicai-tech.com/rss/from/https://linux.do/c/develop/4.rss"] = func() *http.Response { proxy++; return response(`<rss><channel/></rss>`) }
	h.tick(first - 1)
	if daily+direct+proxy != 0 {
		t.Fatal("upgrade bypassed the restart gate")
	}
	h.tick(first)
	if daily != 1 || direct != 0 || proxy != 1 {
		t.Fatal("sources retained their old timetable or bypassed cooling", daily, direct, proxy)
	}
	policy, err := h.store.Policy()
	if err != nil || policy.Version != 3 || policy.EffectiveAt != effective || policy.HourlyDaytimeAt != first {
		t.Fatal("lost sampling boundaries", policy, err)
	}
	var hosts map[string]struct {
		BlockedUntil int64 `json:"blockedUntil"`
	}
	if _, err := h.store.State("upstream", &hosts); err != nil || hosts["api.github.com"].BlockedUntil != until {
		t.Fatal("upgrade shortened an existing cooldown", hosts, err)
	}
	h.tick(first + tracking.Hour)
	if daily != 2 || direct != 1 || proxy != 2 {
		t.Fatal("new schedule did not traverse both hosts", daily, direct, proxy)
	}
	policy, _ = h.store.Policy()
	if policy.HourlyDaytimeAt != first {
		t.Fatal("policy boundary changed on a later tick")
	}
}

// TestSourceAndCandidateFailuresRetryAfterFiveMinutes 先制造来源失败再制造候选失败，分别在截止前后执行，确认五分钟退避和到期恢复。
func TestSourceAndCandidateFailuresRetryAfterFiveMinutes(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-02T08:50:00"))
	first := beijing("2026-10-02T09:00:00")
	failed, requests := true, 0
	h.sources["https://rsshub-balancer.virworks.moe/github/trending/daily/any"] = func() *http.Response {
		requests++
		if failed {
			res := response("unavailable")
			res.StatusCode = 503
			return res
		}
		return response(`<rss><channel><item><link>https://github.com/acme/one</link></item></channel></rss>`)
	}
	h.tick(first)
	failed = false
	h.tick(first + 4*60_000)
	if requests != 1 {
		t.Fatal("source retried too soon", requests)
	}
	h.failed["R_0"] = true
	h.tick(first + 6*60_000)
	if requests != 2 {
		t.Fatal("source did not retry after five minutes", requests)
	}
	var pending []struct {
		NextAt int64 `json:"nextAt"`
	}
	if _, err := h.store.State("pending", &pending); err != nil || len(pending) != 1 {
		t.Fatal("candidate failure lost its retry", pending, err)
	}
	retry := pending[0].NextAt
	if retry < first+11*60_000 || retry > first+12*60_000 {
		t.Fatal("candidate still uses the old retry interval", retry)
	}
	h.failed["R_0"] = false
	h.tick(retry - 1)
	if h.list("github")["total"] != float64(0) {
		t.Fatal("candidate retried too soon")
	}
	h.tick(retry)
	if h.list("github")["total"] != float64(1) {
		t.Fatal("candidate did not retry after five minutes")
	}
}
