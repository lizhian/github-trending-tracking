package tracking

// 本文件缓存尚未收录候选的公开不可访问结果，并提供匿名 REST 复核。
// 它与 linuxdo.go 的 README 不符合缓存互相独立；不能把 README 404 当成仓库不存在。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

const unavailableCachePrefix = "repository_unavailable:"

// unavailableResult 记录候选公开不可访问的确认时间和 24 小时有效期；不表示断言仓库已删除。
type unavailableResult struct {
	CheckedAt int64 `json:"checkedAt"`
	ExpiresAt int64 `json:"expiresAt"`
}

var errPublicRepositoryMissing = errors.New("GitHub repository publicly unavailable")

// unavailableCacheKey 按忽略大小写的仓库名称生成不可访问缓存键，与按稳定 ID 的 README 缓存分开。
func unavailableCacheKey(name string) string { return unavailableCachePrefix + strings.ToLower(name) }

// unavailableCache 读取缓存并检查有效期；同时返回数据、是否有效与错误，调用者必须先处理错误。
func (s *Scheduler) unavailableCache(name string) (unavailableResult, bool, error) {
	var cached unavailableResult
	found, err := s.store.State(unavailableCacheKey(name), &cached)
	return cached, found && cached.ExpiresAt > s.upstream.clock.Now(), err
}

// clearUnavailableCache 删除一个或多个名称/别名的不可访问缓存；主动投稿确认公开可访问后调用。
// db 只要求 ExecContext 方法，因此 *sql.DB 与 *sql.Tx 都可传入，无需继承共同基类。
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

// publicRepository 在带 Token 查询明确 NOT_FOUND 后匿名复核仓库；README 404 不能作为调用依据。
// 只有匿名仓库 404 且未处于主机冷却才返回明确不可访问；其他失败继续等待确认。
// expectedID 非空时要求身份一致，防止仓库改名或名称被复用后误认目标。
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
	// 把 REST 资料转换成观测持久化所用的 GraphQL 形状，复用同一套仓库结构。
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
