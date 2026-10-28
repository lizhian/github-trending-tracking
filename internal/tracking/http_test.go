package tracking_test

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lizhian/github-trending-tracking/internal/tracking"
)

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
	h := tracking.Handler(store, tracking.HTTPOptions{AllowedOrigin: "https://site.test", Now: func() time.Time { return now }})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/projects?type=linux.do", nil))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["items"] == nil || got["languages"] == nil || got["topics"] == nil || got["pages"] != float64(1) {
		t.Fatalf("invalid empty list %s", w.Body)
	}
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

func TestInitialEmptyTypeIsNotCachedAndNonemptyQueriesExpire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tracking.sqlite")
	if err := tracking.Initialize(path); err != nil {
		t.Fatal(err)
	}
	store, err := tracking.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 10, 1, 5, 4, 0, 0, time.UTC)
	h := tracking.Handler(store, tracking.HTTPOptions{Now: func() time.Time { return now }})
	request := func(path string) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		var value map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if request("/api/projects?type=linux.do")["total"] != float64(0) {
		t.Fatal("not initially empty")
	}
	trends := `{"24":{"delta":null,"state":"accumulating","start":1,"end":1,"gaps":[]},"72":{"delta":null,"state":"accumulating","start":1,"end":1,"gaps":[]},"168":{"delta":null,"state":"accumulating","start":1,"end":1,"gaps":[]},"720":{"delta":null,"state":"accumulating","start":1,"end":1,"gaps":[]}}`
	_, err = store.DB.Exec("INSERT INTO projects(id,cycle,name,topics,entered_at,listed_at,observed_at,attempted_at,stars,trends,type,repo_id) VALUES('linux.do:R_1','new-cycle','acme/new','[]',1,1,1,1,100,?,'linux.do','R_1')", trends)
	if err != nil {
		t.Fatal(err)
	}
	if request("/api/projects?type=linux.do")["total"] != float64(1) {
		t.Fatal("initial empty type was cached")
	}
	before := request("/api/projects?type=linux.do&q=missing")
	now = now.Add(599 * time.Second)
	if got := request("/api/projects?type=linux.do&q=missing"); got["asOf"] != before["asOf"] {
		t.Fatal("zero-match query with tracked projects was not cached")
	}
	now = now.Add(time.Second)
	if got := request("/api/projects?type=linux.do&q=missing"); got["asOf"] == before["asOf"] {
		t.Fatal("list cache exceeded 600 seconds")
	}
}
