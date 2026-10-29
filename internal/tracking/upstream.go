package tracking

// 本文件封装上游 HTTP/GraphQL 请求、主机间隔和冷却。
// 先读 Clock 与 text，再读 lookupRepositories；调度和投稿通过 scheduler.go 的锁共享这里的状态。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Clock 抽象“现在”和“可取消等待”，供生产时钟与测试时钟替换。
// Go 接口由使用方需要的方法定义，实现方只需具备同名同签名方法，不写 implements。
type Clock interface {
	Now() int64
	Sleep(context.Context, time.Duration) error
}

// realClock 生产时钟的无状态实现；struct{} 是零字段结构体，无需为它保存状态。
type realClock struct{}

// Now 返回 Unix 毫秒时间；接收者不需要访问字段，因此只写类型而省略变量名。
func (realClock) Now() int64 { return time.Now().UnixMilli() }

// Sleep 等待定时器或 context 取消，先发生的事件结束等待；不会像直接 time.Sleep 那样忽略取消。
func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	// <- 是 channel 接收；ctx.Done() 取消后关闭，timer.C 到期时可读。
	// select 会等待可执行的分支，defer Stop 用于释放不再需要的定时器资源。
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// hostState 保存每个主机的上次请求开始时间和冷却截止时间，以 JSON 持久化跨重启复用。
type hostState struct {
	StartedAt    int64 `json:"startedAt"`
	BlockedUntil int64 `json:"blockedUntil"`
}

const retryInterval int64 = 5 * 60_000

// deferredRequest 表示因主机冷却或发现预算不足而延后，并非实际请求失败；retryAt=0 表示预算耗尽。
type deferredRequest struct{ retryAt int64 }

// Error 满足内置 error 接口唯一的方法 Error() string；调用者用 errors.As 识别延后原因。
func (d *deferredRequest) Error() string {
	if d.retryAt == 0 {
		return "Discovery budget exhausted"
	}
	return "Upstream cooling down"
}

// upstream 共享上游客户端、时钟与主机状态；由 Scheduler 的锁串行访问，不在此处重复加锁。
type upstream struct {
	hosts       map[string]*hostState
	clock       Clock
	client      *http.Client
	rateLimited bool
}

// host 按 URL 主机名取得或创建状态；同主机不同 API 路径共享请求间隔和冷却。
func (u *upstream) host(raw string) *hostState {
	v, e := url.Parse(raw)
	if e != nil {
		return &hostState{}
	}
	key := strings.ToLower(v.Hostname())
	if u.hosts[key] == nil {
		u.hosts[key] = &hostState{}
	}
	return u.hosts[key]
}

// blocked 返回该主机冷却截止的 Unix 毫秒时间，0 表示尚未设置冷却。
func (u *upstream) blocked(raw string) int64 { return u.host(raw).BlockedUntil }

// cooldown 延长冷却至本地下限、已有截止、Retry-After 和额度重置时间中的最大值，绝不缩短已有冷却。
// Retry-After 可为秒数或 HTTP 日期，而 X-RateLimit-Reset 是 Unix 秒，需要转换成毫秒。
func (u *upstream) cooldown(raw string, headers http.Header, duration int64) {
	s := u.host(raw)
	now := u.clock.Now()
	until := max(s.BlockedUntil, now+duration)
	if retry := headers.Get("Retry-After"); retry != "" {
		if seconds, e := strconv.ParseInt(strings.TrimSpace(retry), 10, 64); e == nil && seconds >= 0 {
			until = max(until, now+seconds*1000)
		} else if at, e := http.ParseTime(retry); e == nil {
			until = max(until, at.UnixMilli())
		}
	}
	if headers.Get("X-RateLimit-Remaining") == "0" {
		if reset, e := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64); e == nil {
			until = max(until, reset*1000)
		}
	}
	s.BlockedUntil = until
}

// upstreamHTTPError 保留安全的上游类别和状态码，支持 errors.As 判断，不保存原始正文或秘密。
type upstreamHTTPError struct {
	label  string
	status int
}

// Error 格式化安全类别和 HTTP 状态码，不返回原始上游响应正文。
func (e *upstreamHTTPError) Error() string { return fmt.Sprintf("%s HTTP %d", e.label, e.status) }

// text 执行带超时、主机间隔和冷却约束的 HTTP 请求，成功返回受大小限制的文本。
// body=nil 使用 GET，否则 POST；deadline 是本轮发现的总预算截止时间，0 表示无此额外预算。
// headers 是可变参数；请求失败或限流由调用方决定何时重试，不在这里无限循环。
func (u *upstream) text(ctx context.Context, raw, label string, body []byte, token string, deadline int64, headers ...http.Header) (string, error) {
	parsed, e := url.Parse(raw)
	if e != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", errors.New("invalid upstream URL")
	}
	s := u.host(raw)
	now := u.clock.Now()
	if s.BlockedUntil > now {
		return "", &deferredRequest{s.BlockedUntil}
	}
	timeout := 15 * time.Second
	if label == "GitHub" {
		timeout = 20 * time.Second
	}
	wait := max(int64(0), s.StartedAt+2000-now)
	if deadline > 0 && now+wait+timeout.Milliseconds() > deadline {
		return "", &deferredRequest{}
	}
	if wait > 0 {
		if e = u.clock.Sleep(ctx, time.Duration(wait)*time.Millisecond); e != nil {
			return "", e
		}
	}
	now = u.clock.Now()
	if deadline > 0 && now+timeout.Milliseconds() > deadline {
		return "", &deferredRequest{}
	}
	s.StartedAt = now
	// 请求上下文继承父任务取消并增加单次超时；defer cancel 及时释放计时资源，不必等到超时。
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	method := "GET"
	if body != nil {
		method = "POST"
	}
	req, e := http.NewRequestWithContext(requestCtx, method, raw, strings.NewReader(string(body)))
	if e != nil {
		return "", e
	}
	req.Header.Set("User-Agent", "github-trending-tracking")
	// http.Header 本质是 map[string][]string，一个头可有多个值；这里转发调用方提供的头字段。
	for _, header := range headers {
		for name, values := range header {
			req.Header[name] = values
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, e := u.client.Do(req)
	if e != nil {
		return "", e
	}
	// 即使随后判断 HTTP 状态失败也要关闭响应体，否则影响连接复用并泄漏资源；
	// Client.Do 的 err=nil 只代表传输获得响应，HTTP 4xx/5xx 仍需检查 StatusCode。
	defer res.Body.Close()
	limited := res.StatusCode == 429 || res.Header.Get("Retry-After") != "" || res.Header.Get("X-RateLimit-Remaining") == "0"
	if limited {
		u.rateLimited = true
		u.cooldown(raw, res.Header, retryInterval)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 || (label == "GitHub" && res.StatusCode != http.StatusOK) {
		if label == "GitHub" && res.StatusCode == 403 && !limited {
			b, e := readBounded(res.Body)
			if e != nil {
				return "", e
			}
			lower := strings.ToLower(b)
			if strings.Contains(lower, "secondary rate limit") || strings.Contains(lower, "api rate limit exceeded") {
				u.rateLimited = true
				u.cooldown(raw, res.Header, retryInterval)
			}
		}
		return "", &upstreamHTTPError{label: label, status: res.StatusCode}
	}
	return readBounded(res.Body)
}

// readBounded 最多读取 2,000,001 字节，多读的一字节用于判断是否超过 2,000,000 字节上限。
func readBounded(r io.Reader) (string, error) {
	b, e := io.ReadAll(io.LimitReader(r, 2_000_001))
	if e != nil {
		return "", e
	}
	if len(b) > 2_000_000 {
		return "", errors.New("Upstream response too large")
	}
	return string(b), nil
}

var safeDiagnostic = regexp.MustCompile(`^(GitHub HTTP [0-9]{3}|RSS HTTP [0-9]{3}|HelloGithub HTTP [0-9]{3}|OpenGithubs HTTP [0-9]{3}|GitHub GraphQL unavailable|RSS invalid XML|RSS missing channel|HelloGithub invalid project|Upstream response too large|Upstream empty response|Upstream cooling down)$`)

// safeError 只放行预定义诊断文本，其余统一返回安全提示，避免上游正文、URL 或 Token 泄漏。
func safeError(e error) string {
	if e != nil && safeDiagnostic.MatchString(e.Error()) {
		return e.Error()
	}
	return "Upstream timeout or unavailable"
}

// repository 映射 GitHub GraphQL 仓库数据；匿名嵌套 struct 对应嵌套 JSON，无需为每层另建类型。
// Stars/Private 用指针区分字段缺失或 null 与合法的 0/false，避免把不完整结果当成功。
type repository struct {
	ID          string  `json:"id"`
	Name        string  `json:"nameWithOwner"`
	Description *string `json:"description"`
	Homepage    *string `json:"homepageUrl"`
	Language    *struct {
		Name string `json:"name"`
	} `json:"primaryLanguage"`
	Topics *struct {
		Nodes []struct {
			Topic struct {
				Name string `json:"name"`
			} `json:"topic"`
		} `json:"nodes"`
	} `json:"repositoryTopics"`
	Stars   *int64 `json:"stargazerCount"`
	Private *bool  `json:"isPrivate"`
}

// repositories 封装批量查询；投稿单仓库明确 NOT_FOUND 时转换成可识别的拒绝错误，其余保留结果。
func (u *upstream) repositories(ctx context.Context, token string, targets []string, byID bool, mode string) ([]*repository, error) {
	repos, missing, err := u.lookupRepositories(ctx, token, targets, byID, mode)
	if err == nil && mode == "submission" && len(missing) == 1 && missing[0] {
		return nil, errSubmissionRepositoryMissing
	}
	return repos, err
}

// lookupRepositories 按名称或稳定 ID 批量获取仓库，模式决定只查身份、Star 或完整资料。
// 返回的 repos/missing 与 targets 索引对齐；nil 仓库不必然表示不存在，必须结合目标自身错误路径。
// 允许 GraphQL 部分成功：保留有效节点，限流仍更新冷却，避免一个目标失败抹掉其他成功结果。
func (u *upstream) lookupRepositories(ctx context.Context, token string, targets []string, byID bool, mode string) (repos []*repository, missing []bool, err error) {
	const endpoint = "https://api.github.com/graphql"
	defer func() {
		if err != nil && ctx.Err() == nil {
			var deferred *deferredRequest
			if !errors.As(err, &deferred) {
				u.cooldown(endpoint, nil, retryInterval)
			}
		}
	}()
	fields := "id stargazerCount"
	if mode == "identity" {
		fields = "id nameWithOwner"
	} else if mode == "metadata" || mode == "submission" {
		fields = "id nameWithOwner description homepageUrl primaryLanguage { name } repositoryTopics(first:100) { nodes { topic { name } } } stargazerCount"
	}
	if mode == "submission" {
		fields += " isPrivate"
	}
	query := ""
	if byID {
		// 用 JSON 编码字符串列表，正确处理引号等转义，再放进 GraphQL 文本；不手工拼裸字符串值。
		ids, _ := json.Marshal(targets)
		query = "query { nodes(ids:" + string(ids) + ") { ... on Repository { " + fields + " } } }"
	} else {
		var parts []string
		for i, name := range targets {
			owner, repo, _ := strings.Cut(name, "/")
			o, _ := json.Marshal(owner)
			r, _ := json.Marshal(repo)
			parts = append(parts, fmt.Sprintf("r%d: repository(owner:%s,name:%s) { %s }", i, o, r, fields))
		}
		query = "query { " + strings.Join(parts, " ") + " }"
	}
	body, _ := json.Marshal(map[string]string{"query": query})
	text, e := u.text(ctx, endpoint, "GitHub", body, token, 0)
	if e != nil {
		return nil, nil, e
	}
	var result struct {
		// RawMessage 保存尚未解码的 JSON 字节，允许按别名/节点分别校验部分响应，避免整批过早转换。
		Data   map[string]json.RawMessage `json:"data"`
		Errors []struct {
			Type       string `json:"type"`
			Message    string `json:"message"`
			Path       []any  `json:"path"`
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if e = json.Unmarshal([]byte(text), &result); e != nil {
		return nil, nil, e
	}
	for _, problem := range result.Errors {
		if problem.Type == "RATE_LIMITED" || problem.Extensions.Code == "RATE_LIMITED" || strings.Contains(strings.ToLower(problem.Message), "rate limit") {
			u.rateLimited = true
			u.cooldown(endpoint, nil, retryInterval)
		}
	}
	if mode == "submission" && len(result.Errors) > 0 {
		for _, problem := range result.Errors {
			if problem.Type != "NOT_FOUND" && problem.Extensions.Code != "NOT_FOUND" {
				return nil, nil, errors.New("GitHub GraphQL unavailable")
			}
		}
	}
	if result.Data == nil {
		return nil, nil, errors.New("GitHub GraphQL unavailable")
	}
	var nodes []json.RawMessage
	if byID {
		if len(result.Data["nodes"]) > 0 {
			if e = json.Unmarshal(result.Data["nodes"], &nodes); e != nil {
				return nil, nil, e
			}
		}
	} else {
		for i := range targets {
			nodes = append(nodes, result.Data[fmt.Sprintf("r%d", i)])
		}
	}
	repos = make([]*repository, len(targets))
	missing = make([]bool, len(targets))
	// 逐目标判断：另一仓库的权限错误不能掩盖本目标明确 NOT_FOUND，
	// 也不能把没有明确证据的 null 解释成仓库缺失。
	for i := range targets {
		if i >= len(nodes) || strings.TrimSpace(string(nodes[i])) != "null" {
			continue
		}
		uncertain := false
		for _, problem := range result.Errors {
			notFound := problem.Type == "NOT_FOUND" || problem.Extensions.Code == "NOT_FOUND"
			if len(problem.Path) == 0 {
				if notFound && len(targets) == 1 {
					missing[i] = true
				} else {
					uncertain = true
				}
				continue
			}
			relevant, exact := false, false
			if !byID {
				relevant = problem.Path[0] == fmt.Sprintf("r%d", i)
				exact = len(problem.Path) == 1
			} else if problem.Path[0] == "nodes" {
				// GraphQL path 里的 JSON 数字解码到 any 后是 float64，所以用 float64(i) 对齐节点索引。
			relevant = len(problem.Path) == 1 || problem.Path[1] == float64(i)
				exact = len(problem.Path) == 2
			}
			if relevant {
				if notFound && exact {
					missing[i] = true
				} else {
					uncertain = true
				}
			}
		}
		if uncertain {
			missing[i] = false
		}
	}

	for i := range targets {
		if i >= len(nodes) {
			continue
		}
		var r repository
		if e = json.Unmarshal(nodes[i], &r); e != nil || r.ID == "" || (byID && r.ID != targets[i]) {
			continue
		}
		if mode == "identity" {
			if repoNamePattern.MatchString(r.Name) {
				repos[i] = &r
			}
			continue
		}
		if r.Stars == nil || *r.Stars < 0 || *r.Stars > 9_007_199_254_740_991 {
			continue
		}
		if (mode == "metadata" || mode == "submission") && (!repoNamePattern.MatchString(r.Name) || (r.Topics == nil || r.Topics.Nodes == nil)) {
			continue
		}
		repos[i] = &r
	}
	return repos, missing, nil
}

var repoNamePattern = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)
