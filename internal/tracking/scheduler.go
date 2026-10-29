package tracking

// 本文件编排后台采集：Run 周期调用 Tick，Tick 执行准入门禁、发现、观测和清理。
// collect 较长，可按“合并候选 → 解析身份 → README 准入 → 复用/请求观测 → 保存状态”阅读。
// 采样和投稿共用锁与上游状态，但来源发现、候选收录和存量观测分别记录结果。

import (
	"context"
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// SchedulerOptions 调度依赖和启动配置；Clock/Client 可注入替身，零值由构造函数补默认。
// StartedAt 为 Unix 毫秒，决定普通采集重启后从哪一轮恢复。
type SchedulerOptions struct {
	Token          string
	// 可替换的接口依赖，类似 Java 构造器注入；测试传模拟时钟，生产由 NewScheduler 补 realClock。
	Clock          Clock
	Client         *http.Client
	// 0 表示由时钟取启动时间；清洗模式暂停普通采集并使用 BackupDir 保存清洗前快照。
	StartedAt      int64
	LinuxdoCleanup bool
	BackupDir      string
}

// Scheduler 协调采样、投稿和主机状态的共享所有者；mu 保护跨网络请求的整个上游工作阶段。
type Scheduler struct {
	store      *Store
	options    SchedulerOptions
	upstream   *upstream
	startAt    int64
	mu         sync.Mutex
	cleanupLog string
}

// NewScheduler 补齐依赖并计算严格晚于启动时间的下一时段；构造函数只是普通函数，New 前缀是惯例。
// options 按值传入；其中指针/接口仍可能引用外部对象，不能理解成对依赖做深拷贝。
func NewScheduler(store *Store, options SchedulerOptions) *Scheduler {
	if options.Clock == nil {
		options.Clock = realClock{}
	}
	if options.Client == nil {
		options.Client = &http.Client{}
	}
	if options.StartedAt == 0 {
		options.StartedAt = options.Clock.Now()
	}
	return &Scheduler{store: store, options: options, upstream: &upstream{hosts: map[string]*hostState{}, clock: options.Clock, client: options.Client}, startAt: NextSlot(options.StartedAt)}
}

// Run 串行调用 Tick，扣除本轮耗时后等待至约一分钟间隔；context 取消时结束循环。
func (s *Scheduler) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		started := s.upstream.clock.Now()
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			log.Print("scheduled tracking failed")
		}
		if err := s.upstream.clock.Sleep(ctx, time.Duration(max(int64(0), 60_000-(s.upstream.clock.Now()-started)))*time.Millisecond); err != nil {
			return
		}
	}
}

// Tick 执行一次维护/采集检查；返回 error 供循环记录，取消时不额外报告业务失败。
// (err error) 是命名返回值，defer 在函数返回前可修改它，将收尾持久化错误传回调用者。
func (s *Scheduler) Tick(ctx context.Context) (err error) {
	// finish 是持有阶段锁后的清理回调；立即检查错误，成功后用 defer 保证后续任意返回都执行。
	finish, err := s.lockUpstream()
	if err != nil {
		return err
	}
	now := s.upstream.clock.Now()
	// 普通 return err 会先设置命名返回值，再执行 defer；这里仅在原流程成功时接纳收尾错误。
	// defer 调用的实参在登记时求值；这里使用闭包，在退出时读取 err 的最终值。
	defer func() {
		if e := finish(); err == nil {
			err = e
		}
		if err != nil && ctx.Err() == nil {
			_ = s.store.SetState("scheduler", runState{AttemptedAt: now, Failed: 1, Error: strptr("采集或数据库任务失败")})
		}
	}()
	allowed, err := s.linuxdoGate(ctx)
	if err != nil || !allowed {
		return err
	}
	if _, err = s.store.DB.ExecContext(ctx, `DELETE FROM system_state WHERE key GLOB ? AND json_extract(value,'$.expiresAt')<=?`, unavailableCachePrefix+"*", now); err != nil {
		return err
	}
	if err = s.expireReadmeCache(ctx, now); err != nil {
		return err
	}
	policy, err := s.store.Policy()
	if err != nil {
		return err
	}
	if policy == nil || policy.Version < 3 {
		policy = s.upgradeSamplingPolicy(policy, now)
		if err = s.store.SetState("sampling_policy", policy); err != nil {
			return err
		}
		s.store.listRevision.Add(1)
	}
	result, err := s.store.DB.ExecContext(ctx, "DELETE FROM snapshot_days WHERE day<?", floorDiv(now-Retention, Day))
	if err != nil {
		return err
	}
	pruned, _ := result.RowsAffected()
	collected := false
	if now >= s.startAt {
		collected, err = s.collect(ctx, now, policy)
		if err != nil {
			return err
		}
	}
	cleaned, err := s.cleanup(ctx, now, policy)
	if err != nil {
		return err
	}
	if collected || cleaned || pruned > 0 {
		return s.store.SetState("scheduler", runState{AttemptedAt: now, SucceededAt: ptr(now), Succeeded: 1})
	}
	return nil
}

// upgradeSamplingPolicy 保留第一次规则生效边界，新时刻表从本进程可采集的首个时段生效。
// 投稿也可能初始化规则，因此统一使用 startAt，不能直接用当前时间替换历史边界。
func (s *Scheduler) upgradeSamplingPolicy(policy *SamplingPolicy, now int64) *SamplingPolicy {
	if policy == nil {
		policy = &SamplingPolicy{EffectiveAt: now}
	}
	if policy.Version < 3 {
		policy.Version = 3
		policy.HourlyDaytimeAt = s.startAt
	}
	return policy
}

// collect 合并来源与持久化候选、确认身份与准入、复用同轮观测，再批量更新最多 100 个不同仓库。
// 返回 bool 表示发现/待处理队列/阶段状态发生变化，error 表示流程失败，并不表示每个候选都失败。
// 同仓库两类项目共享请求结果，但分别保留入库时间、追踪周期和历史；各阶段状态也独立保存。
func (s *Scheduler) collect(ctx context.Context, now int64, policy *SamplingPolicy) (bool, error) {
	d, e := s.discover(ctx, now, policy)
	if e != nil {
		return false, e
	}
	var priorPending []*discoveryEntry
	if _, e = s.store.State("pending", &priorPending); e != nil {
		return false, e
	}
	priorJSON := jsonText(priorPending)
	pending := []*discoveryEntry{}
	// map 存指针；通过 previous 修改候选会同时修改 pending 中引用的对象。
	// 这是有意共享状态，不能把它理解成 Java Stream 中不可变记录的拷贝。
	byName := map[string]*discoveryEntry{}
	addPending := func(entry *discoveryEntry) {
		if entry == nil || repositoryName("https://github.com/"+entry.Name, false) == "" {
			return
		}
		if entry.Failure == "" && max(entry.NextAt, entry.ReadmeNextAt) > 0 {
			entry.Failure = "GitHub repository unavailable"
		}
		if entry.Type == "" {
			entry.Type = "github"
		}
		key := entry.Type + ":" + strings.ToLower(entry.Name)
		if previous := byName[key]; previous != nil {
			if previous.RepoID == "" {
				previous.RepoID = entry.RepoID
			}
			previous.NextAt = max(previous.NextAt, entry.NextAt)
			previous.ReadmeNextAt = max(previous.ReadmeNextAt, entry.ReadmeNextAt)
			if previous.Failure == "" {
				previous.Failure = entry.Failure
			}
		} else {
			byName[key] = entry
			pending = append(pending, entry)
		}
	}
	for _, entry := range priorPending {
		addPending(entry)
	}
	for _, entry := range d.entries {
		addPending(entry)
	}
	rows := []ProjectRow{}
	if len(pending) > 0 {
		rows, e = s.store.ProjectRows("")
		if e != nil {
			return false, e
		}
	}
	kept := []*discoveryEntry{}
	for _, entry := range pending {
		if entry.RepoID == "" {
			for _, row := range rows {
				if strings.EqualFold(row.Name, entry.Name) {
					entry.RepoID = row.RepoID
					break
				}
			}
		}
		tracked := false
		for _, row := range rows {
			if row.Type == entry.Type && row.RepoID == entry.RepoID {
				tracked = true
				break
			}
		}
		if !tracked {
			kept = append(kept, entry)
		}
	}
	pending = kept
	// 先将发现进度与新候选一起持久化；后续上游请求中断时，已发现任务不会仅停留在内存。
	if d.changed {
		tx, e := s.store.DB.BeginTx(ctx, nil)
		if e != nil {
			return false, e
		}
		for key, value := range map[string]any{"pending": pending, "discovery": d.status, "discoverySources": d.state, "discovery:github": d.byType["github"], "discovery:linux.do": d.byType["linux.do"]} {
			if e = setTxState(ctx, tx, key, value); e != nil {
				tx.Rollback()
				return false, e
			}
		}
		if e = tx.Commit(); e != nil {
			return false, e
		}
	}
	results := map[string]*runState{}
	prior := map[string]runState{}
	for _, kind := range []string{"collection", "admission"} {
		previous, err := s.priorRun(kind, "")
		if err != nil {
			return false, err
		}
		prior[kind] = previous
		for _, typ := range projectTypes {
			previous, err := s.priorRun(kind, typ)
			if err != nil {
				return false, err
			}
			v := &runState{AttemptedAt: now, SucceededAt: previous.SucceededAt}
			if kind == "collection" {
				v.Version = 2
			}
			results[kind+":"+typ] = v
		}
	}
	admissionAttempted := map[string]bool{}
	record := func(kind, typ string, success bool, message string) {
		if kind == "admission" {
			admissionAttempted[typ] = true
		}
		v := results[kind+":"+typ]
		if success {
			v.Succeeded++
			v.SucceededAt = ptr(now)
		} else {
			v.Failed++
			v.Error = strptr(message)
		}
	}
	// 指针可作 map 键，这里按候选对象身份标记完成；按仓库去重另用规范名或稳定 ID。
	completed := map[*discoveryEntry]bool{}
	unavailable := map[string]unavailableResult{}
	finishUnavailable := func(entry *discoveryEntry) {
		key := unavailableCacheKey(entry.Name)
		if _, found := unavailable[key]; !found {
			checked := s.upstream.clock.Now()
			unavailable[key] = unavailableResult{CheckedAt: checked, ExpiresAt: checked + Day}
		}
		completed[entry] = true
		record("admission", entry.Type, true, "")
	}
	for _, entry := range pending {
		_, cached, err := s.unavailableCache(entry.Name)
		if err != nil {
			return false, err
		}
		if cached {
			completed[entry] = true
			record("admission", entry.Type, true, "")
		}
	}
	readyGitHub := func() bool { return s.upstream.blocked("https://api.github.com/graphql") <= s.upstream.clock.Now() }
	slot := SamplingSlot(now, policy)
	targets := []*discoveryEntry{}
	unresolved := []string{}
	for _, entry := range pending {
		if !completed[entry] && max(entry.NextAt, entry.ReadmeNextAt) <= now && len(targets) < 20 {
			targets = append(targets, entry)
			if entry.RepoID == "" && !slices.Contains(unresolved, strings.ToLower(entry.Name)) {
				unresolved = append(unresolved, strings.ToLower(entry.Name))
			}
		}
	}
	if len(unresolved) > 0 && readyGitHub() {
		identities, missing, err := s.upstream.lookupRepositories(ctx, s.options.Token, unresolved, false, "identity")
		message := "GitHub repository unavailable"
		if err != nil {
			message = safeError(err)
		}
		for _, entry := range targets {
			if entry.RepoID != "" {
				continue
			}
			i := slices.Index(unresolved, strings.ToLower(entry.Name))
			var identity *repository
			if i >= 0 && i < len(identities) {
				identity = identities[i]
			}
			failure := message
			if _, found := unavailable[unavailableCacheKey(entry.Name)]; found {
				completed[entry] = true
				record("admission", entry.Type, true, "")
				continue
			}
			if err == nil && identity == nil && i >= 0 && i < len(missing) && missing[i] {
				var confirmErr error
				identity, confirmErr = s.upstream.publicRepository(ctx, entry.Name, "", "identity")
				if errors.Is(confirmErr, errPublicRepositoryMissing) {
					finishUnavailable(entry)
					continue
				}
				if confirmErr != nil {
					failure = safeError(confirmErr)
				}
			}
			if identity != nil {
				entry.Failure = ""
				entry.NextAt = 0
				entry.RepoID = identity.ID
				entry.Name = identity.Name
				if _, e = s.store.DB.ExecContext(ctx, "UPDATE projects SET name=? WHERE repo_id=? AND name<>?", identity.Name, identity.ID, identity.Name); e != nil {
					return false, e
				}
				for i := range rows {
					if rows[i].RepoID == identity.ID {
						rows[i].Name = identity.Name
					}
				}
			} else {
				entry.NextAt = max(s.upstream.clock.Now()+retryInterval, s.upstream.blocked("https://api.github.com/graphql"))
				entry.Failure = failure
				record("admission", entry.Type, false, failure)
			}
		}
	}
	// 解析身份后再次按“追踪类型+稳定 ID”合并，消除仓库改名/大小写造成的同类型重复。
	canonical := []*discoveryEntry{}
	byID := map[string]*discoveryEntry{}
	selectedTarget := map[*discoveryEntry]bool{}
	for _, entry := range targets {
		selectedTarget[entry] = true
	}
	for _, entry := range pending {
		if completed[entry] {
			continue
		}
		key := entry.Type + ":" + strings.ToLower(entry.Name)
		if entry.RepoID != "" {
			key = entry.Type + ":" + entry.RepoID
		}
		if previous := byID[key]; previous != nil {
			previous.NextAt = max(previous.NextAt, entry.NextAt)
			previous.ReadmeNextAt = max(previous.ReadmeNextAt, entry.ReadmeNextAt)
			if previous.Failure == "" {
				previous.Failure = entry.Failure
			}
			if selectedTarget[entry] {
				selectedTarget[previous] = true
			}
		} else {
			byID[key] = entry
			canonical = append(canonical, entry)
		}
	}
	pending = canonical
	retryByRepo := map[string]int64{}
	for _, entry := range pending {
		if entry.RepoID != "" {
			retryByRepo[entry.RepoID] = max(retryByRepo[entry.RepoID], entry.NextAt)
		}
	}
	for _, entry := range pending {
		entry.NextAt = max(entry.NextAt, retryByRepo[entry.RepoID])
	}
	ready := []*discoveryEntry{}
	reuse := []observation{}
	for _, entry := range pending {
		if selectedTarget[entry] && entry.RepoID != "" && max(entry.NextAt, entry.ReadmeNextAt) <= now {
			ready = append(ready, entry)
		}
	}
	for _, entry := range ready {
		var existing []ProjectRow
		for _, row := range rows {
			if row.RepoID == entry.RepoID {
				existing = append(existing, row)
			}
		}
		var tracked *ProjectRow
		var latest *ProjectRow
		retryAt := int64(0)
		for i := range existing {
			// 按索引取 slice 元素地址，后面保存的是对应项目行；普通 range 的 row 是值副本。
			row := &existing[i]
			if row.Type == entry.Type {
				tracked = row
			}
			if latest == nil || row.ObservedAt > latest.ObservedAt {
				latest = row
			}
			if row.Error != nil && row.AttemptedAt > now-retryInterval {
				retryAt = max(retryAt, row.AttemptedAt+retryInterval)
			}
		}
		if tracked != nil {
			completed[entry] = true
			continue
		}
		// README 准入必须先于同轮观测复用；已有 Star 结果不代表具有 Linux.do 收录资格。
		if entry.Type == "linux.do" {
			qualification, err := s.linuxdoReadme(ctx, entry.RepoID, entry.Name)
			if err != nil {
				if errors.Is(err, errPublicRepositoryMissing) {
					finishUnavailable(entry)
					continue
				}
				entry.Failure = safeError(err)
				entry.ReadmeNextAt = s.readmeRetryAt()
				record("admission", entry.Type, false, safeError(err))
				continue
			}
			entry.ReadmeNextAt = 0
			entry.Failure = ""
			if !qualification.Eligible {
				if e = s.store.SetState(readmeCacheKey(entry.RepoID), qualification); e != nil {
					return false, e
				}
				completed[entry] = true
				record("admission", entry.Type, true, "")
				continue
			}
		}
		// 复用真实观测时间，不把复用动作伪造成一次新观测；新项目 entered_at 仍用本次收录时间。
		if latest != nil && latest.ObservedAt >= slot {
			repo, e := rowRepository(*latest)
			if e != nil {
				return false, e
			}
			reuse = append(reuse, observation{repo: repo, typ: entry.Type, observedAt: latest.ObservedAt, metadataAt: latest.MetadataAt})
			completed[entry] = true
			record("admission", entry.Type, true, "")
		} else if retryAt > 0 {
			entry.NextAt = retryAt
		}
	}
	if e = s.store.saveObservations(ctx, reuse, now, policy); e != nil {
		return false, e
	}
	ids := []string{}
	newEntries := []*discoveryEntry{}
	for _, entry := range ready {
		if !completed[entry] && max(entry.NextAt, entry.ReadmeNextAt) <= now {
			newEntries = append(newEntries, entry)
			if !slices.Contains(ids, entry.RepoID) {
				ids = append(ids, entry.RepoID)
			}
		}
	}
	if readyGitHub() {
		due, e := s.store.DB.QueryContext(ctx, `SELECT repo_id FROM projects WHERE observed_at<? AND (error IS NULL OR attempted_at<=?) GROUP BY repo_id ORDER BY MIN(attempted_at),repo_id LIMIT 100`, slot, now-retryInterval)
		if e != nil {
			return false, e
		}
		for due.Next() {
			var id string
			if e = due.Scan(&id); e != nil {
				due.Close()
				return false, e
			}
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		e = due.Err()
		due.Close()
		if e != nil {
			return false, e
		}
	} else {
		ids = nil
	}
	// 限制的是不同仓库数；每个仓库可更新两个追踪类型，所以最多涉及 200 个追踪项目。
	if len(ids) > 100 {
		ids = ids[:100]
	}
	selected := []ProjectRow{}
	if len(ids) > 0 {
		args := []any{}
		for _, id := range ids {
			args = append(args, id)
		}
		selected, e = s.store.ProjectRows("WHERE repo_id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+")", args...)
		if e != nil {
			return false, e
		}
	}
	needsMetadata := func(id string) bool {
		found := false
		latest := int64(0)
		for _, row := range selected {
			if row.RepoID == id {
				found = true
				latest = max(latest, row.MetadataAt)
			}
		}
		return !found || latest <= now-Day
	}
	// 分成完整元数据和仅 Star 两组请求，沿用元数据 24 小时刷新周期，减少不必要字段获取。
	for _, metadata := range []bool{true, false} {
		if !readyGitHub() {
			break
		}
		group := []string{}
		for _, id := range ids {
			if needsMetadata(id) == metadata {
				group = append(group, id)
			}
		}
		if len(group) == 0 {
			continue
		}
		mode := "stars"
		if metadata {
			mode = "metadata"
		}
		nodes, missing, err := s.upstream.lookupRepositories(ctx, s.options.Token, group, true, mode)
		message := "GitHub repository unavailable"
		if err != nil {
			message = safeError(err)
		}
		repos := map[string]*repository{}
		for _, repo := range nodes {
			if repo != nil {
				repos[repo.ID] = repo
			}
		}
		observations := []observation{}
		for _, id := range group {
			existing := []*ProjectRow{}
			for i := range selected {
				if selected[i].RepoID == id {
					existing = append(existing, &selected[i])
				}
			}
			additions := []*discoveryEntry{}
			for _, entry := range newEntries {
				if entry.RepoID == id {
					additions = append(additions, entry)
				}
			}
			repo := repos[id]
			if repo != nil {
				latestMetadata := int64(0)
				for _, row := range existing {
					latestMetadata = max(latestMetadata, row.MetadataAt)
				}
				if metadata {
					latestMetadata = now
				} else if len(existing) > 0 {
					latest := existing[0]
					for _, row := range existing {
						if row.MetadataAt > latest.MetadataAt {
							latest = row
						}
					}
					shared, err := rowRepository(*latest)
					if err != nil {
						return false, err
					}
					shared.Stars = repo.Stars
					repo = shared
				}
				for _, row := range existing {
					observations = append(observations, observation{repo: repo, previous: row, metadataAt: latestMetadata})
					record("collection", row.Type, true, "")
				}
				for _, entry := range additions {
					observations = append(observations, observation{repo: repo, typ: entry.Type, metadataAt: latestMetadata})
					completed[entry] = true
					record("admission", entry.Type, true, "")
				}
			} else {
				// 只有未收录候选可以明确结束本次收录；已收录项目保留采样错误及旧历史。
				index := slices.Index(group, id)
				for _, entry := range additions {
					entry.Failure = message
					if _, found := unavailable[unavailableCacheKey(entry.Name)]; found {
						completed[entry] = true
						record("admission", entry.Type, true, "")
						continue
					}
					if err == nil && index < len(missing) && missing[index] {
						recovered, confirmErr := s.upstream.publicRepository(ctx, entry.Name, entry.RepoID, mode)
						if errors.Is(confirmErr, errPublicRepositoryMissing) {
							finishUnavailable(entry)
							continue
						}
						if confirmErr == nil {
							observations = append(observations, observation{repo: recovered, typ: entry.Type, metadataAt: now})
							completed[entry] = true
							record("admission", entry.Type, true, "")
							continue
						}
						entry.Failure = safeError(confirmErr)
					}
				}
				for _, row := range existing {
					if _, e = s.store.DB.ExecContext(ctx, "UPDATE projects SET attempted_at=?,error=? WHERE id=? AND cycle=? AND attempted_at<=?", now, message, row.ID, row.Cycle, now); e != nil {
						return false, e
					}
					record("collection", row.Type, false, message)
				}
				for _, entry := range additions {
					if completed[entry] {
						continue
					}
					if entry.Failure == "" {
						entry.Failure = message
					}
					entry.NextAt = max(s.upstream.clock.Now()+retryInterval, s.upstream.blocked("https://api.github.com/graphql"))
					record("admission", entry.Type, false, entry.Failure)
				}
			}
		}
		if e = s.store.saveObservations(ctx, observations, now, policy); e != nil {
			return false, e
		}
	}
	pending = slices.DeleteFunc(pending, func(v *discoveryEntry) bool {
		return completed[v] || unavailable[unavailableCacheKey(v.Name)].ExpiresAt > 0
	})
	for _, typ := range projectTypes {
		v := results["admission:"+typ]
		v.Failed = 0
		v.Error = nil
	}
	for _, entry := range pending {
		if entry.Failure != "" {
			v := results["admission:"+entry.Type]
			v.Failed++
			if v.Error == nil {
				v.Error = strptr(entry.Failure)
			}
		}
	}
	changes := []string{}
	for _, kind := range []string{"collection", "admission"} {
		for _, typ := range projectTypes {
			key := kind + ":" + typ
			previous, err := s.priorRun(kind, typ)
			if err != nil {
				return false, err
			}
			changed := results[key].Succeeded > 0 || results[key].Failed > 0
			if kind == "admission" {
				changed = admissionAttempted[typ] || previous.Failed != results[key].Failed || jsonText(previous.Error) != jsonText(results[key].Error)
			}
			if changed {
				changes = append(changes, key)
			}
		}
	}
	// 候选结束、不可访问缓存和错误状态在同一事务提交，避免移除候选却留下“后台重试”提示。
	pendingChanged := jsonText(pending) != priorJSON
	if pendingChanged || len(changes) > 0 || len(unavailable) > 0 {
		tx, e := s.store.DB.BeginTx(ctx, nil)
		if e != nil {
			return false, e
		}
		for key, cached := range unavailable {
			if e = setTxState(ctx, tx, key, cached); e != nil {
				tx.Rollback()
				return false, e
			}
		}
		if pendingChanged {
			e = setTxState(ctx, tx, "pending", pending)
		}
		for _, kind := range []string{"collection", "admission"} {
			global := runState{AttemptedAt: now, SucceededAt: prior[kind].SucceededAt}
			if kind == "collection" {
				global.Version = 2
			}
			for _, key := range changes {
				if !strings.HasPrefix(key, kind+":") {
					continue
				}
				v := results[key]
				if e == nil {
					e = setTxState(ctx, tx, key, v)
				}
				global.Succeeded += v.Succeeded
				global.Failed += v.Failed
				if v.Succeeded > 0 {
					global.SucceededAt = ptr(now)
				}
				if global.Error == nil {
					global.Error = v.Error
				}
			}
			if kind == "admission" {
				global = runState{AttemptedAt: now, SucceededAt: prior[kind].SucceededAt}
				for _, typ := range projectTypes {
					v := results[kind+":"+typ]
					global.Succeeded += v.Succeeded
					global.Failed += v.Failed
					if v.Succeeded > 0 {
						global.SucceededAt = ptr(now)
					}
					if global.Error == nil {
						global.Error = v.Error
					}
				}
			}
			if e == nil && ((kind == "collection" && global.Succeeded+global.Failed > 0) || (kind == "admission" && (slices.Contains(changes, "admission:github") || slices.Contains(changes, "admission:linux.do") || global.Failed != prior[kind].Failed))) {
				e = setTxState(ctx, tx, kind, global)
			}
		}
		if e != nil {
			tx.Rollback()
			return false, e
		}
		if e = tx.Commit(); e != nil {
			return false, e
		}
		s.store.listRevision.Add(1)
	}
	return d.changed || pendingChanged || len(changes) > 0, nil
}

// lockUpstream 获得采样与投稿共用的互斥锁，加载持久化主机状态，返回结束本阶段的函数。
// 调用者必须执行返回函数：状态变化时保存，随后解锁；不是单纯读取后立刻释放的短锁。
func (s *Scheduler) lockUpstream() (func() error, error) {
	// 互斥锁跨越整个调用方工作阶段；返回的闭包完成持久化后才解锁。
	// Go mutex 不是可重入锁，持锁路径不能再调用一次 lockUpstream。
	s.mu.Lock()
	hosts := map[string]*hostState{}
	if _, err := s.store.State("upstream", &hosts); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if hosts == nil {
		hosts = map[string]*hostState{}
	}
	s.upstream.hosts = hosts
	before := jsonText(hosts)
	return func() error {
		defer s.mu.Unlock()
		if jsonText(hosts) != before {
			return s.store.SetState("upstream", hosts)
		}
		return nil
	}, nil
}
