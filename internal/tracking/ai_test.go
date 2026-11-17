package tracking

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

type aiTransport func(*http.Request) (*http.Response, error)

func (f aiTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAIGenerationCacheAndFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), DatabaseName)
	if err := Initialize(path); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	readme, status, modelStatus := "# A tool for watching GitHub stars", 200, 200
	calls := 0
	client := &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) {
		code, body := status, readme
		if r.URL.Host == "model.test" {
			calls++
			if r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("x-opencode-session") == "" {
				t.Error("missing model headers")
			}
			code = modelStatus
			content := `{"summary":"这是一个追踪开源项目趋势的工具。","tags":["开源","趋势分析","开发工具"]}`
			payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
			body = string(payload)
		} else if !strings.HasSuffix(r.URL.Path, "/readme") {
			code = 200
			body = `{"node_id":"R_test","full_name":"owner/repo","private":false}`
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	scheduler := NewScheduler(store, SchedulerOptions{AI: AIOptions{BaseURL: "https://model.test/v1", Model: "test", APIKey: "test-key"}})
	job := aiJob{RepoID: "R_test", Name: "owner/repo"}
	generate := func() {
		t.Helper()
		if err := scheduler.generateAI(context.Background(), client, job); err != nil {
			t.Fatal(err)
		}
	}
	generate()
	generate()
	if calls != 1 {
		t.Fatalf("unchanged README called model %d times", calls)
	}
	var original aiResult
	store.State("ai_result:R_test", &original)
	readme += "\nNew functionality"
	modelStatus = 429
	if err := scheduler.generateAI(context.Background(), client, job); err == nil {
		t.Fatal("model failure accepted")
	}
	var after aiResult
	store.State("ai_result:R_test", &after)
	if after.Signature != original.Signature {
		t.Fatal("failure committed signature")
	}
	modelStatus = 200
	generate()
	if calls != 3 {
		t.Fatal("failed generation was not retried")
	}
	status = 500
	if err := scheduler.generateAI(context.Background(), client, job); err == nil {
		t.Fatal("README failure treated as missing")
	}
	if calls != 3 {
		t.Fatal("README failure called model")
	}
	status = 404
	description := "Track open source projects"
	job.Description = &description
	generate()
	generate()
	if calls != 4 {
		t.Fatal("description fallback not cached")
	}
	job.Description = nil
	generate()
	store.State("ai_result:R_test", &after)
	if calls != 4 || after.Summary != "暂无项目介绍" || len(after.Tags) != 0 {
		t.Fatal("empty source called model or invented content")
	}
}
