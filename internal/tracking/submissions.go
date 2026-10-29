package tracking

// 本文件负责投稿地址规范化、持久化入队和结果查询。
// HTTP 层在 submissions_http.go，异步处理在 submissions_worker.go；接收成功并不等于已经收录。

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
)

// 可识别的包级错误值；HTTP 层用 errors.Is 判断容量不足，避免依赖中文错误文本。
var ErrSubmissionCapacity = errors.New("投稿队列已满，请稍后再试")

const submissionColumns = "id,name,repo_id,phase,github,linuxdo,repository_attempts,readme_attempts,next_at,created_at,finished_at,message,merged_into"

// Submission 包含投稿持久化字段与对外视图；json:"-" 使内部身份、阶段和次数不进入响应。
// GitHub 与 Linuxdo 分别表达结果，允许先收录 GitHub、再确认 Linux.do。
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

// submissionName 校验 HTTPS GitHub 仓库根地址并统一小写，兼容 .git、尾斜杠和查询参数，拒绝子页面与保留路径。
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
	// 复用发现解析器的保留路径校验，再额外限制必须是仓库根地址。
	parsed := repositoryName("https://github.com/"+name, false)
	if parsed == "" || parsed != name || !repoNamePattern.MatchString(name) {
		return "", parameterError("请输入 GitHub 仓库根地址，不支持子页面")
	}
	return strings.ToLower(name), nil
}

// scanSubmission 通过最小 Scan 接口将数据库字段写入投稿结构，复用于单行和结果集扫描。
func scanSubmission(row interface{ Scan(...any) error }) (Submission, error) {
	var s Submission
	err := row.Scan(&s.ID, &s.Name, &s.RepoID, &s.Phase, &s.GitHub, &s.Linuxdo, &s.RepositoryAttempts, &s.ReadmeAttempts, &s.NextAt, &s.CreatedAt, &s.FinishedAt, &s.Message, &s.MergedInto)
	return s, err
}

// view 按当前时间派生 URL、展示状态和过期时间；值接收者修改的是结构体副本，不直接改变原变量。
// 这是浅复制：指针字段仍指向同一数据，但这里通过重新赋值而不解引用修改旧值。
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

// Submission 按请求 ID 查询投稿，沿合并链追到实际任务，仍返回最初请求的 ID。
// 结果保留七天；最多追溯 100 层防止异常别名链无限循环。
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

// EnqueueSubmission 在事务内合并同名未完成投稿或创建新任务，全站最多 100 条未完成记录。
// 重复投稿优先合并，即使队列已满也可返回已有任务；成功提交后再读取对外视图。
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
	// 已有任务时不需要提交新变更，defer Rollback 会释放这个只读查找事务；直接返回原任务视图。
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

// updateSubmission 在传入事务内写回阶段、结果与重试状态，调用方决定何时和观测一起提交。
func updateSubmission(ctx context.Context, tx *sql.Tx, sub *Submission) error {
	_, err := tx.ExecContext(ctx, `UPDATE submissions SET name=?,repo_id=?,phase=?,github=?,linuxdo=?,repository_attempts=?,readme_attempts=?,next_at=?,finished_at=?,message=?,merged_into=? WHERE id=?`, sub.Name, sub.RepoID, sub.Phase, sub.GitHub, sub.Linuxdo, sub.RepositoryAttempts, sub.ReadmeAttempts, sub.NextAt, sub.FinishedAt, sub.Message, sub.MergedInto, sub.ID)
	return err
}
