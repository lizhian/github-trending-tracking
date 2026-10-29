package tracking

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

const unavailableCachePrefix = "repository_unavailable:"

type unavailableResult struct {
	CheckedAt int64 `json:"checkedAt"`
	ExpiresAt int64 `json:"expiresAt"`
}

var errPublicRepositoryMissing = errors.New("GitHub repository publicly unavailable")

func unavailableCacheKey(name string) string { return unavailableCachePrefix + strings.ToLower(name) }

func (s *Scheduler) unavailableCache(name string) (unavailableResult, bool, error) {
	var cached unavailableResult
	found, err := s.store.State(unavailableCacheKey(name), &cached)
	return cached, found && cached.ExpiresAt > s.upstream.clock.Now(), err
}

func clearUnavailableCache(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, names ...string) error {
	if len(names) == 0 {
		return nil
	}
	args := make([]any, len(names))
	for i, name := range names {
		args[i] = unavailableCacheKey(name)
	}
	_, err := db.ExecContext(ctx, "DELETE FROM system_state WHERE key IN ("+strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")+")", args...)
	return err
}

// Only call after an explicit authenticated NOT_FOUND. README-file 404s do not
// establish that the repository itself is unavailable.
func (u *upstream) publicRepository(ctx context.Context, name, expectedID, mode string) (*repository, error) {
	if !repoNamePattern.MatchString(name) {
		return nil, errors.New("GitHub repository unavailable")
	}
	text, err := u.text(ctx, "https://api.github.com/repos/"+name, "GitHub", nil, "", 0,
		http.Header{"Accept": {"application/vnd.github+json"}, "X-Github-Api-Version": {"2026-03-10"}})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if isHTTPStatus(err, http.StatusNotFound) {
			if until := u.blocked("https://api.github.com/graphql"); until > u.clock.Now() {
				return nil, &deferredRequest{until}
			}
			return nil, errPublicRepositoryMissing
		}
		return nil, err
	}
	var raw struct {
		ID          string    `json:"node_id"`
		Name        string    `json:"full_name"`
		Private     *bool     `json:"private"`
		Stars       *int64    `json:"stargazers_count"`
		Description *string   `json:"description"`
		Homepage    *string   `json:"homepage"`
		Language    *string   `json:"language"`
		Topics      *[]string `json:"topics"`
	}
	if json.Unmarshal([]byte(text), &raw) != nil || raw.ID == "" || !repoNamePattern.MatchString(raw.Name) || raw.Private == nil || *raw.Private || (expectedID != "" && raw.ID != expectedID) {
		return nil, errors.New("GitHub repository unavailable")
	}
	r := &repository{ID: raw.ID, Name: raw.Name, Private: raw.Private, Stars: raw.Stars, Description: raw.Description, Homepage: raw.Homepage}
	if mode == "identity" {
		return r, nil
	}
	if raw.Stars == nil || *raw.Stars < 0 || *raw.Stars > 9_007_199_254_740_991 || raw.Topics == nil {
		return nil, errors.New("GitHub repository unavailable")
	}
	// Reuse the GraphQL-shaped metadata contract used by observation persistence.
	metadata := map[string]any{"primaryLanguage": nil, "repositoryTopics": map[string]any{"nodes": []any{}}}
	if raw.Language != nil {
		metadata["primaryLanguage"] = map[string]string{"name": *raw.Language}
	}
	nodes := []any{}
	for _, topic := range *raw.Topics {
		nodes = append(nodes, map[string]any{"topic": map[string]string{"name": topic}})
	}
	metadata["repositoryTopics"] = map[string]any{"nodes": nodes}
	b, _ := json.Marshal(metadata)
	if err := json.Unmarshal(b, r); err != nil {
		return nil, err
	}
	return r, nil
}
