package tracking_test

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lizhian/github-trending-tracking/internal/tracking"
)

// The existing Worker produces the independent expectations from the same frozen
// export and clock. The TypeScript parity test invokes this Go HTTP seam.
func TestWorkerParity(t *testing.T) {
	directory := os.Getenv("GTT_PARITY_DIR")
	if directory == "" {
		t.Skip("run npm test -- tests/go-parity.test.ts for live Worker comparison")
	}
	var fixture struct {
		Now   int64 `json:"now"`
		Cases []struct {
			Path, Method, Origin string
			Status               int
			Body                 json.RawMessage
		} `json:"cases"`
	}
	raw, err := os.ReadFile(filepath.Join(directory, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "tracking.sqlite")
	if _, err = tracking.ImportD1(filepath.Join(directory, "source.sql"), target, filepath.Join(t.TempDir(), "report.json"), true); err != nil {
		t.Fatal(err)
	}
	store, err := tracking.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := tracking.Handler(store, tracking.HTTPOptions{AllowedOrigin: "https://site.test", Now: func() time.Time { return time.UnixMilli(fixture.Now) }})
	for _, example := range fixture.Cases {
		t.Run(example.Method+example.Path, func(t *testing.T) {
			r := httptest.NewRequest(example.Method, example.Path, nil)
			r.Header.Set("Origin", example.Origin)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != example.Status {
				t.Fatalf("status %d want %d: %s", w.Code, example.Status, w.Body)
			}
			if example.Status == 204 {
				return
			}
			var expected, actual any
			if err = json.Unmarshal(example.Body, &expected); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(w.Body.Bytes(), &actual); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(expected, actual) {
				want, _ := json.MarshalIndent(expected, "", "  ")
				os.WriteFile(filepath.Join(directory, "expected.json"), want, 0600)
				os.WriteFile(filepath.Join(directory, "actual.json"), w.Body.Bytes(), 0600)
				t.Fatalf("Go/Worker HTTP JSON differ for %s; evidence: %s", example.Path, directory)
			}
		})
	}
}
