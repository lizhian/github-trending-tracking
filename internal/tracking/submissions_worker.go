package tracking

// 本文件消费持久化投稿队列，按仓库资料和 README 两个阶段推进。
// 先完成 GitHub 收录再判断 Linux.do 资格，各阶段独立提交；通过 Scheduler 共享请求锁与冷却。

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"strings"
	"time"
)

var errSubmissionRepositoryMissing = errors.New("GitHub repository not found")

// SubmissionWorker 持有共享调度器和容量为一的唤醒 channel；投稿本身存在数据库中，channel 只发送通知。
type SubmissionWorker struct {
	scheduler *Scheduler
	wake      chan struct{}
}

// NewSubmissionWorker 构造投稿消费者，复用已有 Scheduler，因此普通采样与投稿共享冷却和请求间隔。
func NewSubmissionWorker(scheduler *Scheduler) *SubmissionWorker {
	return &SubmissionWorker{scheduler: scheduler, wake: make(chan struct{}, 1)}
}

// Wake 非阻塞发送空结构体通知；已有通知时走 default 合并唤醒，不阻塞 HTTP 请求。
// struct{}{} 是零字段结构体的值，适合只表达事件、不携带数据的 channel。
func (w *SubmissionWorker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Paused 只读检查维护门禁，不等待调度器覆盖网络请求的互斥锁；数据库查询仍受单连接约束。
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

// Run 连续处理到期阶段；无任务或失败时等待唤醒、30 秒计时器或取消信号，再扫描持久队列。
// 通知可合并但任务不会丢，因为真实任务在数据库里，重启也可继续。
func (w *SubmissionWorker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		worked, err := w.ProcessNext(ctx)
		if err != nil && ctx.Err() == nil {
			log.Print("submission processing failed; retrying on next scan")
		}
		// 成功处理一个阶段后立即继续扫下一条，不固定等待 30 秒，让 GitHub 之后的 README 阶段及时推进。
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

// ProcessNext 串行处理最早到期投稿的一个阶段，使用与采样相同的锁保护主机状态。
// GitHub 阶段先提交，使其结果在后续 README 请求前已可见；worked=false 表示本轮未处理任务。
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

// retry 按阶段记录重试，普通失败最多三次、至少间隔五分钟；限流和延后不消耗次数。
// context 取消直接退出，不计失败；README 次数耗尽保留已完成的 GitHub 收录。
func (w *SubmissionWorker) retry(ctx context.Context, sub *Submission, err error) error {
	s := w.scheduler
	if ctx.Err() != nil {
		return ctx.Err()
	}
	now := s.upstream.clock.Now()
	// 普通失败也可能设置主机冷却；只有实际限流或延后才不消耗该阶段的尝试次数。
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

// reject 结束明确不可访问/非公开仓库的投稿，不执行 Linux.do 阶段，只返回安全提示。
func (w *SubmissionWorker) reject(ctx context.Context, sub *Submission) error {
	sub.Phase = "done"
	sub.FinishedAt = ptr(w.scheduler.upstream.clock.Now())
	sub.GitHub = "failed"
	sub.Linuxdo = "skipped"
	sub.Message = "仓库不存在、不是公开仓库或无法访问"
	return w.save(ctx, sub)
}

// repositoryStage 获取公开仓库完整资料、按稳定身份合并别名，再提交 GitHub 收录及下一阶段检查点。
// 成功公开查询会清除不可访问缓存，但不会清除 README 不符合缓存。
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
	// 新的公开查询只解除仓库不可访问缓存；即使随后并入其他任务也要执行，不绕过 README 缓存。
	if err := clearUnavailableCache(ctx, s.store.DB, sub.Name, repo.Name); err != nil {
		return err
	}
	sub.RepoID = strptr(repo.ID)
	canonical := strings.ToLower(repo.Name)
	// 先解析别名合并，再设置唯一的活跃稳定仓库身份，避免同仓库重复处理。
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

// linuxdoStage 已有 Linux.do 项目直接完成；否则校验 README，不符合只结束 Linux.do 阶段。
// 符合时优先复用同轮共享观测，必要时重新请求资料，再提交独立 Linux.do 追踪项目。
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

// admit 确保目标追踪类型存在，复用同轮 Star 但创建独立周期，并原子提交采样规则与投稿进度。
// 已存在的类型不重置保护期；新增成功后递增列表版本，使进程内旧缓存失效。
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
		item := observation{repo: repo, typ: kind, observedAt: now, metadataAt: now}
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

// checkpoint 将主机冷却和投稿进度写入当前事务，与该阶段创建的观测一起成功或失败。
func (w *SubmissionWorker) checkpoint(ctx context.Context, tx *sql.Tx, sub *Submission) error {
	if err := setTxState(ctx, tx, "upstream", w.scheduler.upstream.hosts); err != nil {
		return err
	}
	return updateSubmission(ctx, tx, sub)
}

// save 仅保存投稿检查点而不写观测，复用统一事务入口以保证进度和主机状态一致。
func (w *SubmissionWorker) save(ctx context.Context, sub *Submission) error {
	s := w.scheduler
	return s.store.saveObservationsAnd(ctx, nil, s.upstream.clock.Now(), nil, func(tx *sql.Tx) error { return w.checkpoint(ctx, tx, sub) })
}
