package tracking

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

const readmeCachePrefix = "linuxdo_readme:"
const linuxdoCleanupKey = "linuxdo_cleanup_v1"

type readmeResult struct {
	Eligible  bool   `json:"eligible"`
	Reason    string `json:"reason"`
	CheckedAt int64  `json:"checkedAt"`
	ExpiresAt int64  `json:"expiresAt"`
}

func isHTTPStatus(err error, status int) bool {
	var failure *upstreamHTTPError
	return errors.As(err, &failure) && failure.status == status
}

// Resolve by stable identity before requesting content: names can be renamed or reused.
// Errors are deliberately distinct from a successful, negative qualification.
func (s *Scheduler) linuxdoReadme(ctx context.Context, repoID string) (readmeResult, error) {
	var cached readmeResult
	if found, err := s.store.State(readmeCachePrefix+repoID, &cached); err != nil {
		return readmeResult{}, err
	} else if found && !cached.Eligible && cached.ExpiresAt > s.upstream.clock.Now() {
		return cached, nil
	}
	repos, err := s.upstream.repositories(ctx, s.options.Token, []string{repoID}, true, "identity")
	if err != nil {
		return readmeResult{}, err
	}
	if len(repos) != 1 || repos[0] == nil || repos[0].ID != repoID {
		return readmeResult{}, errors.New("GitHub repository unavailable")
	}
	base := "https://api.github.com/repos/" + repos[0].Name
	headers := http.Header{"Accept": {"application/vnd.github.raw+json"}, "X-Github-Api-Version": {"2026-03-10"}}
	text, err := s.upstream.text(ctx, base+"/readme", "GitHub", nil, s.options.Token, 0, headers)
	missing := false
	if isHTTPStatus(err, http.StatusNotFound) {
		// Metadata access alone does not prove token contents permissions. Confirm
		// public accessibility anonymously, then repeat README without that token.
		var metadata string
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

func (s *Scheduler) readmeRetryAt() int64 {
	return max(s.upstream.clock.Now()+30*60_000, s.upstream.blocked("https://api.github.com/graphql"))
}

func (s *Scheduler) expireReadmeCache(ctx context.Context, now int64) error {
	_, err := s.store.DB.ExecContext(ctx, `DELETE FROM system_state WHERE key GLOB ? AND json_extract(value,'$.expiresAt')<=?`, readmeCachePrefix+"*", now)
	return err
}

func readmeCacheKey(repoID string) string { return readmeCachePrefix + repoID }
