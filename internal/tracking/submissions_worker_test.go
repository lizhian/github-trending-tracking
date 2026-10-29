package tracking

// 本文件为同包测试，验证投稿队列的分阶段提交、失败重试与共享冷却。
// 先读 submissionHarness 的依赖注入，再读两个 step 如何分别完成仓库与 README 阶段。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// submissionTestClock 带互斥锁的模拟时钟，供采样和投稿并发场景安全读写；等待只推进时间，不实际休眠。
type submissionTestClock struct {
	mu sync.Mutex
	at int64
}

// Now 加锁读取当前模拟时间；短方法中的 defer 仍在方法返回前释放锁。
func (c *submissionTestClock) Now() int64       { c.mu.Lock(); defer c.mu.Unlock(); return c.at }

// advance 将模拟时间设置到指定毫秒，常用于精确抵达重试边界。
func (c *submissionTestClock) advance(at int64) { c.mu.Lock(); c.at = at; c.mu.Unlock() }

// Sleep 检查取消后加锁推进模拟时间，不等待真实墙上时间。
func (c *submissionTestClock) Sleep(ctx context.Context, d time.Duration) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	c.mu.Lock()
	c.at += d.Milliseconds()
	c.mu.Unlock()
	return nil
}

// submissionTransport 函数类型的 RoundTripper 适配器，将测试回调注入 http.Client。
type submissionTransport func(*http.Request) (*http.Response, error)

// RoundTrip 把 HTTP 请求交给替身函数，不建立真实网络连接。
func (f submissionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// submissionResponse 构造指定状态和正文的 HTTP 响应，用 NopCloser 补齐 Body.Close。
func submissionResponse(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

const submissionRepository = `{"id":"R1","nameWithOwner":"acme/tool","description":"a tool","homepageUrl":"https://example.com/tool","primaryLanguage":{"name":"Go"},"repositoryTopics":{"nodes":[]},"stargazerCount":100,"isPrivate":false}`

// submissionHarness 组合真实数据库、模拟时间、共享调度器、投稿消费者和可替换响应函数。
type submissionHarness struct {
	store     *Store
	scheduler *Scheduler
	worker    *SubmissionWorker
	clock     *submissionTestClock
	requests  int
	readmes   int
	response  func(*http.Request) (*http.Response, error)
}

// newSubmissionHarness 建立投稿测试环境，拦截 HTTP 请求计数，并允许单个用例覆盖默认成功响应。
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

// success 按 README、按 ID 查询或按名称查询返回正确形状的模拟成功数据。
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

// enqueue 以真实入队 API 创建投稿，自动补齐 GitHub URL 并使用模拟时间。
func (h *submissionHarness) enqueue(t *testing.T, name string) Submission {
	t.Helper()
	sub, err := h.store.EnqueueSubmission(context.Background(), "https://github.com/"+name, h.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// step 执行且要求确实处理一个到期阶段；有错误或未处理任务均使测试立即失败。
func (h *submissionHarness) step(t *testing.T) {
	t.Helper()
	worked, err := h.worker.ProcessNext(context.Background())
	if err != nil || !worked {
		t.Fatalf("worked=%t err=%v", worked, err)
	}
}

// result 按模拟当前时间读取投稿展示结果，统一测试中的失败定位。
func (h *submissionHarness) result(t *testing.T, id string) Submission {
	t.Helper()
	sub, err := h.store.Submission(id, h.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// TestSubmissionAdmissionIndependentStages 验证启动等待不阻塞投稿、GitHub 先提交、Linux.do 复用观测但独立周期，重复不重置保护期而淘汰再入库会重建周期。
func TestSubmissionAdmissionIndependentStages(t *testing.T) {
	// 准备：持久化投稿，时钟仍处于普通采样启动等待内；后续 step 每次只处理一个阶段。
	h := newSubmissionHarness(t)
	sub := h.enqueue(t, "acme/tool")
	// 普通采样启动等待不能推迟已入队投稿的处理。
	if h.scheduler.startAt <= h.clock.Now() {
		t.Fatal("fixture is not in startup wait")
	}
	// 执行与断言：第一个 step 后 GitHub 必须已经提交，即使 Linux.do 仍未完成。
	h.step(t)
	if got := h.result(t, sub.ID); got.GitHub != "tracked" || got.Linuxdo != "pending" {
		t.Fatal(got)
	}
	github, _ := h.store.ProjectRows("WHERE type='github'")
	if len(github) != 1 {
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
	}
	handler := Handler(h.store, HTTPOptions{Now: func() time.Time { return time.UnixMilli(h.clock.Now()) }})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/projects", nil))
	var list ListResult
	if err = json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || strings.Contains(w.Body.String(), `"listedAt"`) {
		t.Fatal("unexpected submitted projects", w.Body, err)
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
	if _, err = h.store.DB.Exec("DELETE FROM projects WHERE type='github'"); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(h.clock.Now() + 2*Hour)
	reentered := h.enqueue(t, "acme/tool")
	h.step(t)
	h.step(t)
	github, _ = h.store.ProjectRows("WHERE type='github'")
	if github[0].Cycle == original.Cycle || h.result(t, reentered.ID).GitHub != "tracked" {
		t.Fatal("reentry reused old cycle")
	}
}

// TestSubmissionStableIdentityAliasesAndResultExpiry 同稳定仓库的不同名称合并后原请求 ID 仍可查，结果到期后别名也不可绕过七天限制。
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
	// 重新制造活跃任务，独立验证稳定 ID 合并，不依赖前面阶段恰好怎样调度。
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
	// 尚未物理清理时查询也必须遵守七天有效期。
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

// TestSubmissionNegativeCacheAndPartialReadmeFailure 分别验证 README 不符合缓存复用/到期再投，
// 以及三次 README 失败只结束为部分成功而保留 GitHub 项目。
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
				if current.Status != "retrying" || current.NextAt != h.clock.Now()+5*60_000 {
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

// TestSubmissionPrivacyFailuresAndRateLimitRecovery 区分私有/缺失、认证失败与限流；限流不计次数，重启后并发采样/投稿仍保留冷却，到期可恢复。
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
				// 重建调度器模拟进程重启，再并发执行普通采样与投稿，验证共享冷却不会被覆盖。
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

// TestSubmissionFiveMinuteRetriesAndUpstreamReset 覆盖普通不可用、HTTP/二级/GraphQL 限流和额度重置，验证退避时间、计次数差异及精确截止恢复。
func TestSubmissionFiveMinuteRetriesAndUpstreamReset(t *testing.T) {
	for _, kind := range []string{"unavailable", "http-limit", "secondary-limit", "graphql-limit", "reset"} {
		t.Run(kind, func(t *testing.T) {
			h := newSubmissionHarness(t)
			now := h.clock.Now()
			wait := int64(5 * 60_000)
			attempts := 0
			h.response = func(r *http.Request) (*http.Response, error) {
				switch kind {
				case "unavailable":
					attempts = 1
					return submissionResponse(503, "unavailable"), nil
				case "secondary-limit":
					return submissionResponse(403, "secondary rate limit"), nil
				case "graphql-limit":
					return submissionResponse(200, `{"errors":[{"type":"RATE_LIMITED"}]}`), nil
				case "reset":
					wait = 20 * 60_000
					res := submissionResponse(403, "limited")
					res.Header.Set("X-RateLimit-Remaining", "0")
					res.Header.Set("X-RateLimit-Reset", fmt.Sprint((now+wait)/1000))
					return res, nil
				default:
					return submissionResponse(429, "limited"), nil
				}
			}
			sub := h.enqueue(t, "acme/tool")
			h.step(t)
			got := h.result(t, sub.ID)
			if got.NextAt != now+wait || got.RepositoryAttempts != attempts {
				t.Fatal("incorrect retry or consumed a rate-limited attempt", got)
			}
			h.clock.advance(got.NextAt - 1)
			if worked, err := h.worker.ProcessNext(context.Background()); err != nil || worked {
				t.Fatal("retried before deadline", worked, err)
			}
			h.response = nil
			h.clock.advance(got.NextAt)
			h.step(t)
			if got := h.result(t, sub.ID); got.GitHub != "tracked" {
				t.Fatal("did not recover at retry deadline", got)
			}
		})
	}
}

// TestSubmissionAdmissionCheckpointIsAtomicAndCancellationIsFree 用触发器令队列更新失败，
// 验证项目和快照一起回滚；再取消请求，确认不消耗次数且重建消费者可继续。
func TestSubmissionAdmissionCheckpointIsAtomicAndCancellationIsFree(t *testing.T) {
	h := newSubmissionHarness(t)
	sub := h.enqueue(t, "acme/tool")
	// 在真实 SQLite 的投稿检查点更新处注入故障，检验事务回滚是否也覆盖已写项目和快照。
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

// TestSubmissionListCacheInvalidatesAndCleanupGateRejects 先生成可缓存列表再收录新项目，验证立即失效；随后设置清洗门禁，确认消费者暂停且不请求上游。
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
	// 清洗门禁是新投稿请求与已入队消费者共同使用的暂停依据；这里继续验证消费者读取。
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
