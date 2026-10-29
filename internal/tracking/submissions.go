package tracking

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
)

var ErrSubmissionCapacity = errors.New("投稿队列已满，请稍后再试")

const submissionColumns = "id,name,repo_id,phase,github,linuxdo,repository_attempts,readme_attempts,next_at,created_at,finished_at,message,merged_into"

type Submission struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	URL                string  `json:"url"`
	Status             string  `json:"status"`
	GitHub             string  `json:"github"`
	Linuxdo            string  `json:"linuxdo"`
	NextAttemptAt      *int64  `json:"nextAttemptAt"`
	ExpiresAt          *int64  `json:"expiresAt"`
	Message            string  `json:"message"`
	RepoID             *string `json:"-"`
	Phase              string  `json:"-"`
	RepositoryAttempts int     `json:"-"`
	ReadmeAttempts     int     `json:"-"`
	NextAt             int64   `json:"-"`
	CreatedAt          int64   `json:"-"`
	FinishedAt         *int64  `json:"-"`
	MergedInto         *string `json:"-"`
}

func submissionName(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "github.com") || u.User != nil || (u.Port() != "" && u.Port() != "443") || u.Opaque != "" || strings.Contains(u.EscapedPath(), "%") {
		return "", parameterError("请输入 https://github.com/owner/repo 仓库地址")
	}
	path := strings.TrimSuffix(u.Path, "/")
	if strings.HasSuffix(strings.ToLower(path), ".git") {
		path = path[:len(path)-4]
	}
	name := strings.TrimPrefix(path, "/")
	// Reuse the discovery parser's reserved-path checks, then require a root URL.
	parsed := repositoryName("https://github.com/"+name, false)
	if parsed == "" || parsed != name || !repoNamePattern.MatchString(name) {
		return "", parameterError("请输入 GitHub 仓库根地址，不支持子页面")
	}
	return strings.ToLower(name), nil
}

func scanSubmission(row interface{ Scan(...any) error }) (Submission, error) {
	var s Submission
	err := row.Scan(&s.ID, &s.Name, &s.RepoID, &s.Phase, &s.GitHub, &s.Linuxdo, &s.RepositoryAttempts, &s.ReadmeAttempts, &s.NextAt, &s.CreatedAt, &s.FinishedAt, &s.Message, &s.MergedInto)
	return s, err
}

func (s Submission) view(now int64) Submission {
	s.URL = "https://github.com/" + s.Name
	s.Status = "queued"
	if s.Phase == "linuxdo" {
		s.Status = "processing"
	}
	if s.NextAt > now && s.FinishedAt == nil {
		s.Status = "retrying"
		s.NextAttemptAt = ptr(s.NextAt)
	}
	if s.FinishedAt != nil {
		s.ExpiresAt = ptr(*s.FinishedAt + 7*Day)
		s.Status = "completed"
		if s.GitHub == "failed" {
			s.Status = "failed"
		} else if s.Linuxdo == "unconfirmed" {
			s.Status = "partial"
		}
	}
	return s
}

func (s *Store) Submission(id string, now int64) (Submission, error) {
	requested := id
	for depth := 0; depth < 100; depth++ {
		sub, err := scanSubmission(s.DB.QueryRow("SELECT "+submissionColumns+" FROM submissions WHERE id=?", id))
		if err != nil {
			return sub, err
		}
		if sub.MergedInto != nil {
			id = *sub.MergedInto
			continue
		}
		if sub.FinishedAt != nil && *sub.FinishedAt+7*Day <= now {
			return Submission{}, sql.ErrNoRows
		}
		sub.ID = requested
		return sub.view(now), nil
	}
	return Submission{}, errors.New("invalid submission alias chain")
}

func (s *Store) EnqueueSubmission(ctx context.Context, raw string, now int64) (Submission, error) {
	name, err := submissionName(raw)
	if err != nil {
		return Submission{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Submission{}, err
	}
	defer tx.Rollback()
	existing, err := scanSubmission(tx.QueryRowContext(ctx, "SELECT "+submissionColumns+" FROM submissions WHERE name=? AND phase IN ('repository','linuxdo')", name))
	if err == nil {
		return existing.view(now), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Submission{}, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM submissions WHERE phase IN ('repository','linuxdo')").Scan(&count); err != nil {
		return Submission{}, err
	}
	if count >= 100 {
		return Submission{}, ErrSubmissionCapacity
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		return Submission{}, err
	}
	id := hex.EncodeToString(token[:])
	_, err = tx.ExecContext(ctx, "INSERT INTO submissions(id,name,next_at,created_at) VALUES(?,?,?,?)", id, name, now, now)
	if err != nil {
		return Submission{}, err
	}
	if err = tx.Commit(); err != nil {
		return Submission{}, err
	}
	return s.Submission(id, now)
}

func updateSubmission(ctx context.Context, tx *sql.Tx, sub *Submission) error {
	_, err := tx.ExecContext(ctx, `UPDATE submissions SET name=?,repo_id=?,phase=?,github=?,linuxdo=?,repository_attempts=?,readme_attempts=?,next_at=?,finished_at=?,message=?,merged_into=? WHERE id=?`, sub.Name, sub.RepoID, sub.Phase, sub.GitHub, sub.Linuxdo, sub.RepositoryAttempts, sub.ReadmeAttempts, sub.NextAt, sub.FinishedAt, sub.Message, sub.MergedInto, sub.ID)
	return err
}

func (s *Store) saveSubmission(ctx context.Context, sub *Submission) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = updateSubmission(ctx, tx, sub); err != nil {
		return err
	}
	return tx.Commit()
}
