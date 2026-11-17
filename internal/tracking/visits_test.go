package tracking_test

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lizhian/github-trending-tracking/internal/tracking"
)

func TestVisitsConcurrentRolloverAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tracking.sqlite")
	if err := tracking.Initialize(path); err != nil {
		t.Fatal(err)
	}
	store, err := tracking.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	now := time.Date(2026, 11, 18, 15, 59, 59, 0, time.UTC)
	h := tracking.Handler(store, tracking.HTTPOptions{Now: func() time.Time { return now }})
	request := func(method string) tracking.VisitCounts {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/api/visits", nil))
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %d %s", method, w.Code, w.Body)
		}
		var counts tracking.VisitCounts
		if err := json.Unmarshal(w.Body.Bytes(), &counts); err != nil {
			t.Error(err)
		}
		return counts
	}
	if got := request("GET"); got != (tracking.VisitCounts{}) {
		t.Fatalf("initial: %+v", got)
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { request("POST") })
	}
	wg.Wait()
	if got := request("GET"); got.Today != 20 || got.Total != 20 {
		t.Fatalf("concurrent: %+v", got)
	}
	now = now.Add(time.Second) // 北京时间午夜，UTC 日期还未改变。
	if got := request("GET"); got.Today != 0 || got.Total != 20 {
		t.Fatalf("midnight read: %+v", got)
	}
	if got := request("POST"); got.Today != 1 || got.Total != 21 {
		t.Fatalf("midnight visit: %+v", got)
	}
	store.Close()
	store, err = tracking.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	h = tracking.Handler(store, tracking.HTTPOptions{Now: func() time.Time { return now }})
	if got := request("GET"); got.Today != 1 || got.Total != 21 {
		t.Fatalf("reopened: %+v", got)
	}
}
