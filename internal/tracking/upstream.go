package tracking

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

type Clock interface {
	Now() int64
	Sleep(context.Context, time.Duration) error
}
type realClock struct{}

func (realClock) Now() int64 { return time.Now().UnixMilli() }
func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type hostState struct {
	StartedAt    int64 `json:"startedAt"`
	BlockedUntil int64 `json:"blockedUntil"`
}
type deferredRequest struct{ retryAt int64 }

func (d *deferredRequest) Error() string {
	if d.retryAt == 0 {
		return "Discovery budget exhausted"
	}
	return "Upstream cooling down"
}

type upstream struct {
	hosts  map[string]*hostState
	clock  Clock
	client *http.Client
}

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
func (u *upstream) blocked(raw string) int64 { return u.host(raw).BlockedUntil }
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

type upstreamHTTPError struct {
	label  string
	status int
}

func (e *upstreamHTTPError) Error() string { return fmt.Sprintf("%s HTTP %d", e.label, e.status) }

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
	defer res.Body.Close()
	limited := res.StatusCode == 429 || res.Header.Get("Retry-After") != "" || res.Header.Get("X-RateLimit-Remaining") == "0"
	if limited {
		u.cooldown(raw, res.Header, Hour)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 || (label == "GitHub" && res.StatusCode != http.StatusOK) {
		if label == "GitHub" && res.StatusCode == 403 && !limited {
			b, e := readBounded(res.Body)
			if e != nil {
				return "", e
			}
			lower := strings.ToLower(b)
			if strings.Contains(lower, "secondary rate limit") || strings.Contains(lower, "api rate limit exceeded") {
				u.cooldown(raw, res.Header, Hour)
			}
		}
		return "", &upstreamHTTPError{label: label, status: res.StatusCode}
	}
	return readBounded(res.Body)
}
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

func safeError(e error) string {
	if e != nil && safeDiagnostic.MatchString(e.Error()) {
		return e.Error()
	}
	return "Upstream timeout or unavailable"
}

type repository struct {
	ID          string  `json:"id"`
	Name        string  `json:"nameWithOwner"`
	Description *string `json:"description"`
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
	Stars *int64 `json:"stargazerCount"`
}

func (u *upstream) repositories(ctx context.Context, token string, targets []string, byID bool, mode string) (repos []*repository, err error) {
	const endpoint = "https://api.github.com/graphql"
	defer func() {
		if err != nil {
			var deferred *deferredRequest
			if !errors.As(err, &deferred) {
				u.cooldown(endpoint, nil, 30*60_000)
			}
		}
	}()
	fields := "id stargazerCount"
	if mode == "identity" {
		fields = "id nameWithOwner"
	} else if mode == "metadata" {
		fields = "id nameWithOwner description primaryLanguage { name } repositoryTopics(first:100) { nodes { topic { name } } } stargazerCount"
	}
	query := ""
	if byID {
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
		return nil, e
	}
	var result struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors []struct {
			Type       string `json:"type"`
			Message    string `json:"message"`
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if e = json.Unmarshal([]byte(text), &result); e != nil {
		return nil, e
	}
	for _, problem := range result.Errors {
		if problem.Type == "RATE_LIMITED" || problem.Extensions.Code == "RATE_LIMITED" || strings.Contains(strings.ToLower(problem.Message), "rate limit") {
			u.cooldown(endpoint, nil, Hour)
		}
	}
	if result.Data == nil {
		return nil, errors.New("GitHub GraphQL unavailable")
	}
	var nodes []json.RawMessage
	if byID {
		if len(result.Data["nodes"]) > 0 {
			if e = json.Unmarshal(result.Data["nodes"], &nodes); e != nil {
				return nil, e
			}
		}
	} else {
		for i := range targets {
			nodes = append(nodes, result.Data[fmt.Sprintf("r%d", i)])
		}
	}
	repos = make([]*repository, len(targets))
	for i := range targets {
		if i >= len(nodes) {
			continue
		}
		var r repository
		if e = json.Unmarshal(nodes[i], &r); e != nil || r.ID == "" {
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
		if mode == "metadata" && (!repoNamePattern.MatchString(r.Name) || (r.Topics == nil || r.Topics.Nodes == nil)) {
			continue
		}
		repos[i] = &r
	}
	return repos, nil
}

var repoNamePattern = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)
