package tracking

// 本文件判断主 README 是否包含 Linux.do 标识，并复用 24 小时不符合缓存。
// “明确不符合”与“请求失败待确认”是不同结果；调用者决定如何事务保存它们。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

const readmeCachePrefix = "linuxdo_readme:"
const linuxdoCleanupKey = "linuxdo_cleanup_v1"

// readmeResult README 准入的明确结果；不符合结果带 24 小时有效期，网络失败通过 error 表达而不伪造否定。
type readmeResult struct {
	Eligible  bool   `json:"eligible"`
	Reason    string `json:"reason"`
	CheckedAt int64  `json:"checkedAt"`
	ExpiresAt int64  `json:"expiresAt"`
}

// isHTTPStatus 通过 errors.As 解开错误链，判断是否属于指定 HTTP 状态；比匹配错误字符串可靠。
func isHTTPStatus(err error, status int) bool {
	var failure *upstreamHTTPError
	return errors.As(err, &failure) && failure.status == status
}

// linuxdoReadme 先按稳定 ID 确认当前仓库名，再读取 GitHub 识别的主 README，判断字面标识 https://linux.do。
// 优先复用尚未过期的不符合缓存；404 需匿名验证仓库身份与 README，以区分权限不足和确实缺文件。
// 请求失败返回 error，明确不符合返回 Eligible=false 且 error=nil；此函数不负责写入新缓存。
func (s *Scheduler) linuxdoReadme(ctx context.Context, repoID string, names ...string) (readmeResult, error) {
	var cached readmeResult
	if found, err := s.store.State(readmeCachePrefix+repoID, &cached); err != nil {
		return readmeResult{}, err
	} else if found && !cached.Eligible && cached.ExpiresAt > s.upstream.clock.Now() {
		return cached, nil
	}
	repos, absent, err := s.upstream.lookupRepositories(ctx, s.options.Token, []string{repoID}, true, "identity")
	if err != nil {
		return readmeResult{}, err
	}
	if len(repos) == 1 && repos[0] == nil && len(absent) == 1 && absent[0] && len(names) > 0 {
		repos[0], err = s.upstream.publicRepository(ctx, names[0], repoID, "identity")
		if err != nil {
			return readmeResult{}, err
		}
	}
	if len(repos) != 1 || repos[0] == nil || repos[0].ID != repoID {
		return readmeResult{}, errors.New("GitHub repository unavailable")
	}
	base := "https://api.github.com/repos/" + repos[0].Name
	headers := http.Header{"Accept": {"application/vnd.github.raw+json"}, "X-Github-Api-Version": {"2026-03-10"}}
	text, err := s.upstream.text(ctx, base+"/readme", "GitHub", nil, s.options.Token, 0, headers)
	missing := false
	if isHTTPStatus(err, http.StatusNotFound) {
		// 能读元数据不代表 Token 具有内容读取权限；先匿名确认仓库公开可读，
		// 再不带 Token 重试 README，才能确认文件确实缺失。
		var metadata string
		// 这里 Token 传空字符串，主动改用匿名身份复核；有 Token 的 404 可能只是权限不足。
		metadata, err = s.upstream.text(ctx, base, "GitHub", nil, "", 0)
		if err != nil {
			return readmeResult{}, err
		}
		var repo struct {
			ID      string `json:"node_id"`
			Name    string `json:"full_name"`
			Private *bool  `json:"private"`
		}
		if json.Unmarshal([]byte(metadata), &repo) != nil || repo.ID != repoID || repo.Private == nil || *repo.Private || !repoNamePattern.MatchString(repo.Name) {
			return readmeResult{}, errors.New("GitHub repository unavailable")
		}
		text, err = s.upstream.text(ctx, "https://api.github.com/repos/"+repo.Name+"/readme", "GitHub", nil, "", 0, headers)
		if isHTTPStatus(err, http.StatusNotFound) {
			missing, err = true, nil
		}
	}
	if err != nil {
		return readmeResult{}, err
	}
	checkedAt := s.upstream.clock.Now()
	// 字面子串检查并不验证 URL 域名边界；如 https://linux.docker.example 也包含标识。
	// 这是当前业务规则，测试会固定这个行为，不能用“优化解析”悄悄改变准入标准。
	result := readmeResult{Eligible: !missing && strings.Contains(text, "https://linux.do"), CheckedAt: checkedAt}
	if !result.Eligible {
		result.Reason = "missing-marker"
		if missing {
			result.Reason = "missing-readme"
		}
		result.ExpiresAt = checkedAt + Day
	}
	return result, nil
}

// readmeRetryAt 取当前时间加五分钟与 GitHub 主机冷却的较大值，作为 README 下次可重试时间。
func (s *Scheduler) readmeRetryAt() int64 {
	return max(s.upstream.clock.Now()+retryInterval, s.upstream.blocked("https://api.github.com/graphql"))
}

// expireReadmeCache 删除已到期的不符合缓存；删除不触发主动检查，之后发现或投稿才重新校验。
func (s *Scheduler) expireReadmeCache(ctx context.Context, now int64) error {
	_, err := s.store.DB.ExecContext(ctx, `DELETE FROM system_state WHERE key GLOB ? AND json_extract(value,'$.expiresAt')<=?`, readmeCachePrefix+"*", now)
	return err
}

// readmeCacheKey 按稳定仓库 ID 生成 README 缓存键，仓库改名不会绕过缓存。
func readmeCacheKey(repoID string) string { return readmeCachePrefix + repoID }
