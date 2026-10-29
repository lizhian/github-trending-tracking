package tracking

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

// SubmissionControl is supplied by the same process as the sampler.
type SubmissionControl interface {
	Paused(context.Context) (bool, error)
	Wake()
}

type submissionLimiter struct {
	mu       sync.Mutex
	requests map[string][]int64
}

func (l *submissionLimiter) allow(ip string, now int64) (bool, int) {
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
	// Never evict live limits, which would let a rotating client reset them.
	if len(times) == 0 && len(l.requests) >= 10_000 {
		return false, 60
	}
	l.requests[ip] = append(times, now)
	return true, 0
}

// ParseProxyNetworks validates the optional comma-separated trusted CIDRs.
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
	// Walk from the connection toward the client, stopping at the first untrusted hop.
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
