package tracking

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"
)

// 搜索主配额可跨重启恢复且不影响GraphQL；通用限流继续阻止同主机请求。
func TestSearchQuotaIsolation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		retry    string
		isolated bool
	}{
		{"primary rejected", 403, "", true},
		{"last successful quota", 200, "", true},
		{"secondary 429", 429, "", false},
		{"retry after", 403, "3600", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := &submissionTestClock{at: 1_000_000}
			calls := 0
			u := &upstream{clock: clock, hosts: map[string]*hostState{}, client: &http.Client{Transport: submissionTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path != "/search/repositories" {
					return submissionResponse(200, `{}`), nil
				}
				res := submissionResponse(tc.status, `{}`)
				res.Header.Set("X-RateLimit-Resource", "search")
				res.Header.Set("X-RateLimit-Remaining", "0")
				res.Header.Set("X-RateLimit-Reset", strconv.FormatInt((clock.Now()+3_600_000)/1000, 10))
				if tc.retry != "" {
					res.Header.Set("Retry-After", tc.retry)
				}
				return res, nil
			})}}
			const searchURL = "https://api.github.com/search/repositories?q=language%3AC%2B%2B"
			_, _ = u.text(context.Background(), searchURL, "GitHub", nil, "secret", 0)
			if u.rateLimited == tc.isolated {
				t.Fatal("primary quota affected global rate limit flag")
			}
			saved, _ := json.Marshal(u.hosts)
			u.hosts = nil
			if err := json.Unmarshal(saved, &u.hosts); err != nil {
				t.Fatal(err)
			}
			_, err := u.text(context.Background(), searchURL, "GitHub", nil, "secret", 0)
			var deferred *deferredRequest
			if !errors.As(err, &deferred) || deferred.retryAt < 4_600_000 {
				t.Fatal("quota cooldown lost", err)
			}
			_, err = u.text(context.Background(), "https://api.github.com/graphql", "GitHub", []byte(`{}`), "secret", 0)
			if tc.isolated && (err != nil || calls != 2) {
				t.Fatal("Search blocked observations", err, calls)
			}
			if !tc.isolated && (!errors.As(err, &deferred) || calls != 1) {
				t.Fatal("global cooldown ignored", err, calls)
			}
		})
	}
}
