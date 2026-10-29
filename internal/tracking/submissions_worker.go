package tracking

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"strings"
	"time"
)

var errSubmissionRepositoryMissing = errors.New("GitHub repository not found")

type SubmissionWorker struct {
	scheduler *Scheduler
	wake      chan struct{}
}

func NewSubmissionWorker(scheduler *Scheduler) *SubmissionWorker {
	return &SubmissionWorker{scheduler: scheduler, wake: make(chan struct{}, 1)}
}

func (w *SubmissionWorker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// This check is read-only and never waits for the sampler's network operations.
func (w *SubmissionWorker) Paused(ctx context.Context) (bool, error) {
	s := w.scheduler
	if s.options.LinuxdoCleanup {
		return true, nil
	}
	var progress linuxdoCleanupState
	found, err := s.store.State(linuxdoCleanupKey, &progress)
	if err != nil {
		return true, err
	}
	if found {
		return progress.CompletedAt == 0, nil
	}
	var count int
	err = s.store.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects WHERE type='linux.do'").Scan(&count)
	return count > 0, err
}

func (w *SubmissionWorker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		worked, err := w.ProcessNext(ctx)
		if err != nil && ctx.Err() == nil {
			log.Print("submission processing failed; retrying on next scan")
		}
		if err == nil && worked {
			continue
		}
		timer := time.NewTimer(30 * time.Second)
		select {
		case <-ctx.Done():
		case <-w.wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// ProcessNext commits one stage, so GitHub admission is visible before README requests.
func (w *SubmissionWorker) ProcessNext(ctx context.Context) (worked bool, err error) {
	s := w.scheduler
	finish, err := s.lockUpstream()
	if err != nil {
		return false, err
	}
	defer func() {
		if e := finish(); err == nil {
			err = e
		}
	}()
	if s.options.LinuxdoCleanup {
		return false, nil
	}
	allowed, err := s.linuxdoGate(ctx)
	if err != nil || !allowed {
		return false, err
	}
	now := s.upstream.clock.Now()
	s.upstream.rateLimited = false
	if _, err = s.store.DB.ExecContext(ctx, "DELETE FROM submissions WHERE finished_at<=?", now-7*Day); err != nil {
		return false, err
	}
	sub, err := scanSubmission(s.store.DB.QueryRowContext(ctx, "SELECT "+submissionColumns+" FROM submissions WHERE phase IN ('repository','linuxdo') AND next_at<=? ORDER BY created_at,id LIMIT 1", now))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if sub.Phase == "repository" {
		err = w.repositoryStage(ctx, &sub)
	} else {
		err = w.linuxdoStage(ctx, &sub)
	}
	return true, err
}

func (w *SubmissionWorker) retry(ctx context.Context, sub *Submission, err error) error {
	s := w.scheduler
	if ctx.Err() != nil {
		return ctx.Err()
	}
	now := s.upstream.clock.Now()
	// A host-wide cooldown can also follow an ordinary failure; only actual rate
	// limits and deferred requests are exempt from the per-stage attempt budget.
	var deferred *deferredRequest
	rateLimited := errors.As(err, &deferred) || s.upstream.rateLimited
	if !rateLimited {
		if sub.Phase == "repository" {
			sub.RepositoryAttempts++
		} else {
			sub.ReadmeAttempts++
		}
	}
	attempts := sub.RepositoryAttempts
	if sub.Phase == "linuxdo" {
		attempts = sub.ReadmeAttempts
	}
	if attempts >= 3 {
		sub.Phase = "done"
		sub.FinishedAt = ptr(now)
		if sub.GitHub != "tracked" {
			sub.GitHub = "failed"
			sub.Linuxdo = "skipped"
			sub.Message = "仓库资料暂时无法获取，请稍后重新投稿"
		} else {
			sub.Linuxdo = "unconfirmed"
			sub.Message = "GitHub 已收录，Linux.do 资格待确认，可重新投稿重试"
		}
	} else {
		sub.NextAt = s.readmeRetryAt()
		sub.Message = "上游暂不可用，等待重试"
	}
	log.Printf("submission phase=%s diagnostic=%s", sub.Phase, safeError(err))
	return w.save(ctx, sub)
}

func (w *SubmissionWorker) reject(ctx context.Context, sub *Submission) error {
	sub.Phase = "done"
	sub.FinishedAt = ptr(w.scheduler.upstream.clock.Now())
	sub.GitHub = "failed"
	sub.Linuxdo = "skipped"
	sub.Message = "仓库不存在、不是公开仓库或无法访问"
	return w.save(ctx, sub)
}

func (w *SubmissionWorker) repositoryStage(ctx context.Context, sub *Submission) error {
	s := w.scheduler
	repos, err := s.upstream.repositories(ctx, s.options.Token, []string{sub.Name}, false, "submission")
	if errors.Is(err, errSubmissionRepositoryMissing) {
		return w.reject(ctx, sub)
	}
	if err != nil {
		return w.retry(ctx, sub, err)
	}
	if len(repos) != 1 || repos[0] == nil || repos[0].Private == nil {
		return w.retry(ctx, sub, errors.New("GitHub GraphQL unavailable"))
	}
	repo := repos[0]
	if *repo.Private {
		return w.reject(ctx, sub)
	}
	sub.RepoID = strptr(repo.ID)
	canonical := strings.ToLower(repo.Name)
	// Resolve aliases before setting the unique active stable identity.
	target, err := scanSubmission(s.store.DB.QueryRowContext(ctx, "SELECT "+submissionColumns+" FROM submissions WHERE id<>? AND phase IN ('repository','linuxdo') AND (repo_id=? OR name=?) ORDER BY created_at,id LIMIT 1", sub.ID, repo.ID, canonical))
	if err == nil {
		sub.Phase = "alias"
		sub.MergedInto = strptr(target.ID)
		return w.save(ctx, sub)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	sub.Name = canonical
	now := s.upstream.clock.Now()
	sub.Phase = "linuxdo"
	sub.GitHub = "tracked"
	sub.NextAt = now
	sub.Message = "GitHub 已收录，正在确认 Linux.do 资格"
	return w.admit(ctx, sub, repo, "github")
}

func (w *SubmissionWorker) linuxdoStage(ctx context.Context, sub *Submission) error {
	s := w.scheduler
	if sub.RepoID == nil {
		return errors.New("submission identity missing")
	}
	existing, err := s.store.ProjectRows("WHERE type='linux.do' AND repo_id=?", *sub.RepoID)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		sub.Linuxdo = "tracked"
		sub.Phase = "done"
		sub.FinishedAt = ptr(s.upstream.clock.Now())
		sub.Message = "两类项目均已收录"
		return w.save(ctx, sub)
	}
	result, err := s.linuxdoReadme(ctx, *sub.RepoID)
	if err != nil {
		return w.retry(ctx, sub, err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	now := s.upstream.clock.Now()
	if !result.Eligible {
		sub.Linuxdo = "ineligible"
		sub.Phase = "done"
		sub.FinishedAt = ptr(now)
		sub.Message = "GitHub 已收录，README 不符合 Linux.do 收录资格"
		return s.store.saveObservationsAnd(ctx, nil, now, nil, func(tx *sql.Tx) error {
			if err := setTxState(ctx, tx, readmeCacheKey(*sub.RepoID), result); err != nil {
				return err
			}
			return w.checkpoint(ctx, tx, sub)
		})
	}
	rows, err := s.store.ProjectRows("WHERE repo_id=? ORDER BY observed_at DESC", *sub.RepoID)
	if err != nil {
		return err
	}
	var repo *repository
	policy, err := s.store.Policy()
	if err != nil {
		return err
	}
	if len(rows) > 0 && rows[0].ObservedAt >= SamplingSlot(now, policy) {
		repo, err = rowRepository(rows[0])
	} else {
		var repos []*repository
		repos, err = s.upstream.repositories(ctx, s.options.Token, []string{*sub.RepoID}, true, "submission")
		if err == nil && len(repos) == 1 && repos[0] != nil && repos[0].Private != nil && !*repos[0].Private {
			repo = repos[0]
		} else if err == nil {
			err = errors.New("GitHub GraphQL unavailable")
		}
	}
	if err != nil {
		return w.retry(ctx, sub, err)
	}
	sub.Linuxdo = "tracked"
	sub.Phase = "done"
	sub.FinishedAt = ptr(s.upstream.clock.Now())
	sub.Message = "两类项目均已收录"
	return w.admit(ctx, sub, repo, "linux.do")
}

func (w *SubmissionWorker) admit(ctx context.Context, sub *Submission, repo *repository, kind string) error {
	s := w.scheduler
	now := s.upstream.clock.Now()
	rows, err := s.store.ProjectRows("WHERE repo_id=? ORDER BY observed_at DESC", repo.ID)
	if err != nil {
		return err
	}
	policy, err := s.store.Policy()
	if err != nil {
		return err
	}
	policy = s.upgradeSamplingPolicy(policy, now)
	items := []observation{}
	tracked := false
	for _, row := range rows {
		if row.Type == kind {
			tracked = true
		}
	}
	if !tracked {
		item := observation{repo: repo, typ: kind, listedAt: ptr(int64(0)), observedAt: now, metadataAt: now}
		if len(rows) > 0 && rows[0].ObservedAt >= SamplingSlot(now, policy) {
			item.repo, err = rowRepository(rows[0])
			if err != nil {
				return err
			}
			item.observedAt = rows[0].ObservedAt
			item.metadataAt = rows[0].MetadataAt
		}
		items = append(items, item)
	}
	err = s.store.saveObservationsAnd(ctx, items, now, policy, func(tx *sql.Tx) error {
		if err := setTxState(ctx, tx, "sampling_policy", policy); err != nil {
			return err
		}
		return w.checkpoint(ctx, tx, sub)
	})
	if err == nil && !tracked {
		s.store.listRevision.Add(1)
	}
	return err
}

func (w *SubmissionWorker) checkpoint(ctx context.Context, tx *sql.Tx, sub *Submission) error {
	if err := setTxState(ctx, tx, "upstream", w.scheduler.upstream.hosts); err != nil {
		return err
	}
	return updateSubmission(ctx, tx, sub)
}

func (w *SubmissionWorker) save(ctx context.Context, sub *Submission) error {
	s := w.scheduler
	return s.store.saveObservationsAnd(ctx, nil, s.upstream.clock.Now(), nil, func(tx *sql.Tx) error { return w.checkpoint(ctx, tx, sub) })
}
