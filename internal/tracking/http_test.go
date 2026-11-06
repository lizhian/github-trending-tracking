package tracking_test

// 本文件用 httptest 在内存中调用真实 Handler，验证查询边界与缓存。
// NewRecorder 记录状态码/响应头/正文，不需启动服务器；时钟通过函数注入以精确验证失效边界。

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lizhian/github-trending-tracking/internal/tracking"
)

// TestEmptyDatabaseIsExplicitAndTypeQueriesRemainReadOnly 验证缺失库不被隐式创建、写锁排他、空列表有明确数组，以及查询参数/方法/来源/路径边界。
func TestEmptyDatabaseIsExplicitAndTypeQueriesRemainReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tracking.sqlite")
	if _, err := tracking.Open(path); err == nil {
		t.Fatal("missing production database was created")
	}
	if err := tracking.Initialize(path); err != nil {
		t.Fatal(err)
	}
	store, err := tracking.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := tracking.Open(path); err == nil {
		t.Fatal("second writer opened same directory")
	}
	now := time.Date(2026, 10, 1, 5, 4, 0, 0, time.UTC)
	lists := tracking.NewListCache(store, func() time.Time { return now })
	if err := lists.Refresh(); err != nil {
		t.Fatal(err)
	}
	h := tracking.Handler(store, tracking.HTTPOptions{Lists: lists, AllowedOrigin: "https://site.test", Now: func() time.Time { return now }})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/projects?type=linux.do", nil))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["items"] == nil || got["languages"] == nil || got["topics"] == nil || got["pages"] != nil {
		t.Fatalf("invalid empty list %s", w.Body)
	}
	// 用一张用例表检查不同 method/path/origin；t.Errorf 记录失败后继续其他用例，t.Fatal 则终止当前测试。
	for _, tt := range []struct {
		method, path, origin string
		status               int
	}{
		{"GET", "/api/projects?type=other", "", 400},
		{"GET", "/api/projects?q=a&q=b", "", 400},
		{"GET", "/api/projects?other=1", "", 400},
		{"POST", "/api/projects", "", 405},
		{"OPTIONS", "/api/projects", "https://site.test", 204},
		{"GET", "/api/projects", "https://other.test", 403},
		{"GET", "/api/projects/missing/history", "", 404},
		{"GET", "/api/projects/missing/history?window=0", "", 400},
	} {
		r := httptest.NewRequest(tt.method, tt.path, nil)
		r.Header.Set("Origin", tt.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tt.status {
			t.Errorf("%s: %d %s", tt.path, w.Code, w.Body)
		}
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("browser cache enabled")
	}
	store.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	reopened, err := tracking.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened.Close()
}

func TestListCacheIsExplicitFullAndCompressed(t *testing.T) {
	store := newStore(t)
	clock := &testClock{beijing("2026-11-05T12:00:00")}
	cache := tracking.NewListCache(store, func() time.Time { return time.UnixMilli(clock.at) })
	h := tracking.Handler(store, tracking.HTTPOptions{Lists: cache, Now: func() time.Time { return time.UnixMilli(clock.at) }})
	request := func(encoding string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/projects?type=github", nil)
		r.Header.Set("Accept-Encoding", encoding)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request(""); w.Code != 503 {
		t.Fatal("not preparing", w.Code)
	}
	if err := cache.Refresh(); err != nil {
		t.Fatal(err)
	}
	empty := request("").Body.String()
	harness := &collectionHarness{store: store}
	seedMatureProjects(t, harness, 105, clock.at)
	if w := request(""); w.Body.String() != empty {
		t.Fatal("query rebuilt cached empty list")
	}
	clock.at += 600000
	if err := cache.Refresh(); err != nil {
		t.Fatal(err)
	}
	plain := request("")
	var result tracking.ListResult
	if err := json.Unmarshal(plain.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 105 || result.AsOf != clock.at {
		t.Fatalf("not full %d", len(result.Items))
	}
	zipped := request("gzip")
	reader, err := gzip.NewReader(zipped.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(body) != plain.Body.String() || zipped.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(zipped.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatal("gzip mismatch", err)
	}
	if w := request("gzip;q=0, *;q=1"); w.Header().Get("Content-Encoding") != "" {
		t.Fatal("ignored gzip refusal")
	}
	// A failed build must leave the successful version readable.
	if _, err := store.DB.Exec("UPDATE projects SET topics='broken'"); err != nil {
		t.Fatal(err)
	}
	if err := cache.Refresh(); err == nil {
		t.Fatal("expected generation failure")
	}
	if w := request(""); w.Body.String() != plain.Body.String() {
		t.Fatal("failed refresh replaced cache")
	}
}
