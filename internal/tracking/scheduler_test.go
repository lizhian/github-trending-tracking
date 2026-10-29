package tracking_test

// 本文件通过外部测试包 tracking_test 验证公开的调度与 HTTP 行为。
// 与 Java 常见的同包测试不同，后缀 _test 的包名隔离未导出符号，只能导入 tracking 后使用大写 API。
// 同目录的外部测试文件共享这里的 fixture；建议先读 testClock、transportFunc、newStore，再读第一个 Test。
// Go 自动发现 *_test.go 中签名为 func TestXxx(t *testing.T) 的测试，不需要 JUnit 注解。

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

// testClock 手动推进的毫秒时钟，供串行测试替换生产 Clock；这里没有锁，不用于并发访问。
type testClock struct{ at int64 }

// Now 返回测试时间，不读取机器真实时间。
func (c *testClock) Now() int64 { return c.at }

// Sleep 立即推进模拟时间而不真正休眠，再返回取消状态；可快速验证主机间隔和超时预算。
func (c *testClock) Sleep(ctx context.Context, d time.Duration) error {
	c.at += d.Milliseconds()
	return ctx.Err()
}

// transportFunc 以函数为底层类型的 HTTP 替身；具备 RoundTrip 后可赋给 http.Client.Transport。
type transportFunc func(*http.Request) (*http.Response, error)

// RoundTrip 调用保存的函数返回模拟响应，满足 http.RoundTripper，无需 mock 框架或真实网络。
func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// response 创建 200 响应；strings.Reader 只有 Read，用 io.NopCloser 包装后才满足 Body 的 io.ReadCloser 要求。
func response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

// newStore 创建隔离临时 SQLite，并登记测试结束时关闭；调用者无需手动管理测试目录。
// t.Helper 让失败定位到调用处；t.Cleanup 随测试及其子测试结束执行，作用域不同于辅助函数自己的 defer。
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

// beijing 把无时区日期字符串按 +08:00 解析成 Unix 毫秒；固定测试输入写错属于测试代码错误，直接 panic。
func beijing(s string) int64 {
	t, e := time.Parse(time.RFC3339, s+"+08:00")
	if e != nil {
		panic(e)
	}
	return t.UnixMilli()
}

// requestJSON 在内存中调用真实 HTTP Handler 并解码响应，不监听端口。
// 默认解码到 any 的 JSON 数字是 float64，因此下面断言常写 float64(1)，不是 Go 隐式数值转换。
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

// TestSchedulerWaitsForNextSlotAndAdmitsWithActualObservation 验证启动不立即采集，到下一应采时段才入库，快照使用实际观测时间且响应不泄漏 Token。
func TestSchedulerWaitsForNextSlotAndAdmitsWithActualObservation(t *testing.T) {
	// 准备：使用临时真库、假时钟和假 HTTP；与 mock DAO 不同，实际 SQL 和序列化仍会执行。
	s := newStore(t)
	c := &testClock{beijing("2026-10-01T11:10:00")}
	start := c.at
	// 闭包读取 h 的当前字段，因此测试稍后修改 stars/failed 就能影响后续响应；
	// 所有外部 URL 最终都被 Transport 接管，不会误发到真实 GitHub。
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
	// 执行：先在启动时间 Tick，再在边界前后推进时间，验证是否真的发生收录。
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
	// 断言：从公开 API 检查入库时间、观测时间和 Star，最后再检查历史与凭据隔离。
	got := requestJSON(t, s, c, "/api/projects")
	if got["total"] != float64(1) {
		t.Fatal(got)
	}
	// 测试已预期响应形状，直接类型断言失败会 panic 并使测试失败；
	// 生产处理未知输入通常应先用 value, ok 形式检查类型。
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

// mustJSON 把测试中已知可编码的数据变为 JSON 文本，用于构造响应与比较状态；不是通用错误处理范例。
func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// collectionHarness 组合真实 SQLite、假时钟和假 HTTP Transport 的测试装置。
// 类似测试 fixture；可以更改 sources、stars、failed 等字段制造场景，无需启动真实 GitHub 服务。
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

// newCollectionHarness 建立已通过清洗门禁的测试环境；根据请求 GraphQL 字段生成对应节点，保留真实调度与数据库流程。
func newCollectionHarness(t *testing.T, start int64) *collectionHarness {
	h := &collectionHarness{t: t, store: newStore(t), clock: &testClock{start}, sources: map[string]func() *http.Response{}, names: []string{"acme/one", "acme/two"}, stars: []int64{100, 200}, failed: map[string]bool{}}
	// 普通采集用例默认已通过存量清洗；清洗专项测试会另行移除这个完成标志。
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
	h.scheduler = tracking.NewScheduler(h.store, tracking.SchedulerOptions{Clock: h.clock, Client: client, StartedAt: start, Token: "private-token-secret"})
	return h
}

// tick 将测试时钟定位到指定毫秒并执行一次完整调度；失败立即终止当前测试。
func (h *collectionHarness) tick(at int64) {
	h.t.Helper()
	h.clock.at = at
	if e := h.scheduler.Tick(context.Background()); e != nil {
		h.t.Fatal(e)
	}
}

// list 通过公开列表 API 查询指定追踪类型，验证持久化数据最终如何展示。
func (h *collectionHarness) list(typ string) map[string]any {
	return requestJSON(h.t, h.store, h.clock, "/api/projects?type="+typ)
}

// history 通过公开历史 API 读取 30 天窗口，避免只检查内部实现而漏掉响应错误。
func (h *collectionHarness) history(id string) map[string]any {
	return requestJSON(h.t, h.store, h.clock, "/api/projects/"+id+"/history?window=720")
}

// TestAttachmentLinksNeverBecomeCandidatesAndOldPendingIsPersistentlyRemoved 验证附件链接不成为仓库候选，
// 旧 pending 中的附件即使没有新来源执行也会持久移除。
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
	// 下一轮没有来源到期；仅清理旧 pending 也必须独立持久化，不能依赖新发现触发。
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

// TestCandidateAndSamplingFailuresHaveIndependentStatus 分别制造候选收录与已收录项目采样失败，确认独立报错、独立恢复，候选仍保留重试。
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

// TestLegacyMixedStatusUsesActualProjectErrorsBeforeNextSamplingSlot 构造旧混合状态，
// 确认查询能从项目行还原真实采样失败，筛选结果也不能掩盖全类型异常。
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
	// 即使筛选隐藏了失败项目，也必须继续显示当前追踪类型的更新异常。
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

// TestSharedObservationKeepsIndependentHistoryAndAdmissionTime 先收录 GitHub 后收录 Linux.do，
// 确认复用当前轮观测但不复制旧历史，入库时间与观测时间分别保留。
func TestSharedObservationKeepsIndependentHistoryAndAdmissionTime(t *testing.T) {
	start := beijing("2026-10-01T08:50:00")
	h := newCollectionHarness(t, start)
	h.homepages = map[int]any{0: "https://example.com/one", 1: "https://example.com/two"}
	linux := "https://linux.do/c/develop/4.rss"
	unavailable := func() *http.Response { r := response("private-source-body"); r.StatusCode = 503; return r }
	h.sources[linux] = unavailable
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
	// 距离上次元数据刷新不足一天、只请求 Star 的轮次，也应能正确收录新追踪类型。
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

// TestFailedAdmissionSharesRetryAcrossNewTypesAndRediscovery 同仓库新类型与重新发现共享仓库级重试截止时间，不能通过另一入口提前请求；到期后两类均可收录。
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
	// 准备：尚无已收录项目时加入另一类型候选，模拟发现后共享重试的场景。
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

// TestRestartAcrossMidnightRetainsLongHostCooldownAndNeverRevivesOldData 模拟跨日重启，
// 验证上游长冷却保留、旧观测仍过期、净增量允许为负，整点重启也不回放当前轮。
func TestRestartAcrossMidnightRetainsLongHostCooldownAndNeverRevivesOldData(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T20:50:00"))
	h.tick(beijing("2026-10-01T21:00:00"))
	old := h.history("github:R_0")["points"]
	// 上游要求的持久冷却可能长于重启门禁，不能被下一采样时段覆盖。
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
	// 恰逢应采整点重启，也必须等严格晚于启动的下一时段。
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

// TestNewestOnlyCollectionKeepsGapAndFailuresKeepGoodObservations 跨过多个轮次只采最新真实观测并记录缺测；请求失败保留旧快照，到退避期限后才能恢复。
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

// seedMatureProjects 批量写入可比较的项目与历史供淘汰测试；末项刻意处于保护期，前几项使用低评分便于观察淘汰顺序。
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

// TestCleanupRunsDuringStartupWaitProtectsYoungProjectsAndReentryStartsNewHistory 确认启动等待期间仍可常规淘汰、保护年轻项目；淘汰项目再发现时开启新历史，
// 不能继承旧周期。
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

// TestCleanupOrdersByScoreDeltaStarsEntryTimeAndID 逐个改变评分、净增量、Star、入库时间与 ID，表驱动验证淘汰排序的每一级决胜规则。
func TestCleanupOrdersByScoreDeltaStarsEntryTimeAndID(t *testing.T) {
	// 表驱动测试把输入与预期写成结构体 slice；每行通过 t.Run 生成可单独定位的子测试。
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
			// range 也能遍历非负整数：这里 i 依次为 0、1，不是遍历名为 2 的集合。
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

// TestLinuxMultipleFeedsAndAuthoritativeSourceParsing 验证七个 RSS 全部遍历后再准入、合并去重；各趋势源只使用其约定的权威链接位置。
func TestLinuxMultipleFeedsAndAuthoritativeSourceParsing(t *testing.T) {
	h := newCollectionHarness(t, beijing("2026-10-01T08:50:00"))
	h.names = []string{"acme/one", "acme/two", "acme/dependency", "acme/detail"}
	h.stars = []int64{100, 200, 300, 400}
	reads := 0
	for i, feed := range linuxFeeds {
		h.sources[feed] = func() *http.Response {
			if reads%len(linuxFeeds) != i {
				t.Fatal("feeds requested out of order", i, reads)
			}
			reads++
			if i == 0 && reads <= len(linuxFeeds) {
				return response(`<rss><channel><item><description>https://github.com/acme/one/tree/main https://github.com.evil.com/no/repo</description></item></channel></rss>`)
			}
			if i == len(linuxFeeds)-1 {
				return response(`<rss xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><item><description>https://github.com/ACME/one.git</description><content:encoded>&lt;a href=&quot;https://github.com/acme/dependency/issues/1&quot;&gt;Dependency&lt;/a&gt;</content:encoded></item></channel></rss>`)
			}
			return response(`<rss><channel/></rss>`)
		}
	}
	transport := h.client.Transport
	h.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.github.com" && (reads == 0 || reads%len(linuxFeeds) != 0) {
			t.Fatal("repository processing started before RSS traversal finished", reads)
		}
		return transport.RoundTrip(r)
	})
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
	if reads != len(linuxFeeds) {
		t.Fatal("successful feed ended traversal early", reads)
	}
	lookups := 0
	for _, q := range h.seenQueries {
		lookups += strings.Count(q, `repository(owner:"acme",name:"one")`)
		lookups += strings.Count(q, `repository(owner:"ACME",name:"one")`)
	}
	if lookups != 1 {
		t.Fatal("duplicate repositories were queried", lookups, h.seenQueries)
	}
	// 首个入口在下一轮为空，也必须继续遍历后续入口，不能把空 RSS 当成提前结束条件。
	h.tick(beijing("2026-10-01T11:01:00"))
	status := h.list("linux.do")["status"].(map[string]any)["discovery"].(map[string]any)
	if reads != 2*len(linuxFeeds) || status["failed"] != float64(0) {
		t.Fatal(reads, status)
	}
}

// TestDiscoveryBudgetDefersHelloDetailsWithoutFailures 让详情请求消耗模拟时间，确认三分钟预算用尽仅延后，不报真实失败，后续轮次可继续完成。
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
	// 后续轮次继续完成因预算延后的详情任务。
	h.tick(first + 10*60_000)
	h.tick(first + 20*60_000)
	got := h.list("github")
	if got["total"] != float64(1) {
		t.Fatal(got)
	}
}

// TestHundredRepositoryBatchesUpdateBothTypesAndCompletedRoundStaysIdle 为 500 个仓库各建两类项目，
// 分五批每批 100 仓库更新两类，完成后空闲扫描不改状态。
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

// TestPartialGraphQLResultsSurviveRateLimitsAndRespectLongerRetryAfter 模拟部分节点成功且限流，
// 确认成功快照保留、失败节点等待更长 Retry-After 后恢复。
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

// TestLateObservationCannotWriteIntoReplacementTrackingCycle 在上游请求期间替换追踪周期，确认旧请求结果不能覆盖替代周期的 Star 和历史。
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

// TestHomepageFollowsMetadataRefresh 验证项目网站随 24 小时元数据刷新而更新，单独 Star 采样不提前刷新，并区分 null 与空字符串。
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
