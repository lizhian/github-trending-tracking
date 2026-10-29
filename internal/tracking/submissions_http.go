package tracking

// 本文件提供投稿 HTTP 接口、可信代理地址识别与每 IP 限流。
// HTTP 只负责入队与查询，后台工作通过 SubmissionControl 接口唤醒，避免请求等待 GitHub。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
)

// SubmissionControl HTTP 层所需的最小后台接口，由同进程的 SubmissionWorker 隐式实现。
// 接口放在使用方可降低耦合，测试可只提供 Paused/Wake 两个方法。
type SubmissionControl interface {
	Paused(context.Context) (bool, error)
	Wake()
}

// submissionLimiter 用互斥锁保护按 IP 保存的请求时间窗口；它是进程内限流，不是数据库持久配额。
type submissionLimiter struct {
	mu       sync.Mutex
	requests map[string][]int64
}

// allow 删除一分钟前的记录，每 IP 最多允许五次，返回是否放行与 Retry-After 秒数。
// 活跃 IP 达到一万时拒绝新 IP，不驱逐有效额度，避免轮换 IP 清空限制。
func (l *submissionLimiter) allow(ip string, now int64) (bool, int) {
	// map 不是并发安全容器；HTTP 请求可能并发调用 allow，需要整段“检查+记录”原子执行。
	// Mutex 零值即可用，不需要 Java synchronized 对象那样额外创建锁实例。
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.requests == nil {
		l.requests = map[string][]int64{}
	}
	for key, times := range l.requests {
		first := 0
		for first < len(times) && times[first] <= now-60_000 {
			first++
		}
		if first == len(times) {
			delete(l.requests, key)
		} else {
			l.requests[key] = times[first:]
		}
	}
	times := l.requests[ip]
	if len(times) >= 5 {
		return false, max(1, int((times[0]+60_000-now+999)/1000))
	}
	// 不驱逐仍有效的额度记录，否则轮换客户端可能借驱逐机制重置限制。
	if len(times) == 0 && len(l.requests) >= 10_000 {
		return false, 60
	}
	l.requests[ip] = append(times, now)
	return true, 0
}

// ParseProxyNetworks 解析可选、逗号分隔的可信代理 CIDR；空配置得到 nil slice，遍历 nil slice 是安全的。
func ParseProxyNetworks(raw string) ([]netip.Prefix, error) {
	var networks []netip.Prefix
	for _, part := range strings.Split(raw, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		network, err := netip.ParsePrefix(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		networks = append(networks, network)
	}
	return networks, nil
}

// clientAddress 默认采用连接对端地址；仅当对端可信时从右向左检查 X-Forwarded-For，停在首个不可信节点。
// 不直接相信用户可伪造的最左端头字段；Unmap 将 IPv4 映射地址统一成 IPv4。
func clientAddress(r *http.Request, networks []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	trusted := func(ip netip.Addr) bool {
		for _, network := range networks {
			if network.Contains(ip) {
				return true
			}
		}
		return false
	}
	peer = peer.Unmap()
	if !trusted(peer) {
		return peer.String()
	}
	// 从连接对端向客户端回溯，遇到首个不可信节点就停止，不能盲信任意转发头。
	chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(chain) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(chain[i]))
		if err != nil {
			return peer.String()
		}
		peer = addr.Unmap()
		if !trusted(peer) {
			break
		}
	}
	return peer.String()
}

// submissionAPI 处理投稿预检、POST 入队与 GET 状态，校验限流、维护门禁、媒体类型和单个 JSON 对象。
// 接收后先提交数据库，再 Wake 后台并返回 202；该状态不代表已完成 GitHub/README 请求。
func (h *handler) submissionAPI(w http.ResponseWriter, r *http.Request) {
	fail := func(code int, message string) { writeJSON(w, code, map[string]string{"error": message}) }
	root := r.URL.Path == "/api/submissions"
	methods := "GET, OPTIONS"
	if root {
		methods = "POST, OPTIONS"
	}
	w.Header().Set("Allow", methods)
	if r.Method == "OPTIONS" {
		w.Header().Set("Access-Control-Allow-Methods", methods)
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(204)
		return
	}
	now := h.options.Now().UnixMilli()
	if root && r.Method == "POST" {
		if ok, retry := h.limiter.allow(clientAddress(r, h.options.TrustedProxies), now); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			fail(429, "投稿过于频繁，请稍后再试")
			return
		}
		if h.options.Submissions == nil {
			fail(503, "投稿处理暂不可用")
			return
		}
		paused, err := h.options.Submissions.Paused(r.Context())
		if err != nil || paused {
			w.Header().Set("Retry-After", "60")
			fail(503, "系统维护中，请稍后投稿")
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			fail(415, "请使用 JSON 提交仓库地址")
			return
		}
		var input struct {
			URL string `json:"url"`
		}
		// 先限制请求体字节数，再解码唯一一个对象；DisallowUnknownFields 拒绝多余字段。
		// 第二次 Decode 必须返回 io.EOF，避免拼接两个 JSON 值被误当合法请求。
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&input)
		if err == nil {
			var extra any
			if e := decoder.Decode(&extra); e != io.EOF {
				if e == nil {
					e = errors.New("extra JSON")
				}
				err = e
			}
		}
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				fail(413, "投稿请求过大")
			} else {
				fail(400, "投稿格式无效")
			}
			return
		}
		sub, err := h.store.EnqueueSubmission(r.Context(), input.URL, now)
		if err != nil {
			var invalid parameterError
			if errors.As(err, &invalid) {
				fail(400, string(invalid))
			} else if errors.Is(err, ErrSubmissionCapacity) {
				w.Header().Set("Retry-After", "60")
				fail(503, err.Error())
			} else {
				fail(503, "投稿暂时不可用，请稍后重试")
			}
			return
		}
		h.options.Submissions.Wake()
		writeJSON(w, 202, sub)
		return
	}
	if !root && r.Method == "GET" {
		id := strings.TrimPrefix(r.URL.Path, "/api/submissions/")
		if len(id) != 32 || strings.Contains(id, "/") {
			fail(404, "投稿不存在或结果已过期")
			return
		}
		sub, err := h.store.Submission(id, now)
		if errors.Is(err, sql.ErrNoRows) {
			fail(404, "投稿不存在或结果已过期")
			return
		}
		if err != nil {
			fail(503, "投稿状态暂不可用")
			return
		}
		writeJSON(w, 200, sub)
		return
	}
	fail(405, "请求方法不被允许")
}
