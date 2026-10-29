package tracking

import (
	"context"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

type SchedulerOptions struct {
	Token, FallbackURL string
	Clock              Clock
	Client             *http.Client
	StartedAt          int64
	LinuxdoCleanup     bool
	BackupDir          string
}
type Scheduler struct {
	store      *Store
	options    SchedulerOptions
	upstream   *upstream
	startAt    int64
	mu         sync.Mutex
	cleanupLog string
}

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
func (s *Scheduler) Tick(ctx context.Context) (err error) {
	finish, err := s.lockUpstream()
	if err != nil {
		return err
	}
	now := s.upstream.clock.Now()
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
	if err = s.expireReadmeCache(ctx, now); err != nil {
		return err
	}
	policy, err := s.store.Policy()
	if err != nil {
		return err
	}
	if policy == nil {
		policy = &SamplingPolicy{Version: 2, EffectiveAt: now}
		if err = s.store.SetState("sampling_policy", policy); err != nil {
			return err
		}
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
	byName := map[string]*discoveryEntry{}
	addPending := func(entry *discoveryEntry) {
		if entry == nil || repositoryName("https://github.com/"+entry.Name, false) == "" {
			return
		}
		if entry.Type == "" {
			entry.Type = "github"
		}
		key := entry.Type + ":" + strings.ToLower(entry.Name)
		if previous := byName[key]; previous != nil {
			if entry.ListedAt > previous.ListedAt {
				previous.ListedAt = entry.ListedAt
				previous.Name = entry.Name
			}
			if previous.RepoID == "" {
				previous.RepoID = entry.RepoID
			}
			previous.NextAt = max(previous.NextAt, entry.NextAt)
			previous.ReadmeNextAt = max(previous.ReadmeNextAt, entry.ReadmeNextAt)
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
				if _, e = s.store.DB.ExecContext(ctx, "UPDATE projects SET listed_at=MAX(listed_at,?) WHERE id=? AND cycle=?", entry.ListedAt, row.ID, row.Cycle); e != nil {
					return false, e
				}
				tracked = true
				break
			}
		}
		if !tracked {
			kept = append(kept, entry)
		}
	}
	pending = kept
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
	record := func(kind, typ string, success bool, message string) {
		v := results[kind+":"+typ]
		if success {
			v.Succeeded++
			v.SucceededAt = ptr(now)
		} else {
			v.Failed++
			v.Error = strptr(message)
		}
	}
	readyGitHub := func() bool { return s.upstream.blocked("https://api.github.com/graphql") <= s.upstream.clock.Now() }
	slot := SamplingSlot(now, policy)
	targets := []*discoveryEntry{}
	unresolved := []string{}
	for _, entry := range pending {
		if max(entry.NextAt, entry.ReadmeNextAt) <= now && len(targets) < 20 {
			targets = append(targets, entry)
			if entry.RepoID == "" && !slices.Contains(unresolved, strings.ToLower(entry.Name)) {
				unresolved = append(unresolved, strings.ToLower(entry.Name))
			}
		}
	}
	if len(unresolved) > 0 && readyGitHub() {
		identities, err := s.upstream.repositories(ctx, s.options.Token, unresolved, false, "identity")
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
			if identity != nil {
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
				entry.NextAt = max(s.upstream.clock.Now()+30*60_000, s.upstream.blocked("https://api.github.com/graphql"))
				record("admission", entry.Type, false, message)
			}
		}
	}
	canonical := []*discoveryEntry{}
	byID := map[string]*discoveryEntry{}
	selectedTarget := map[*discoveryEntry]bool{}
	for _, entry := range targets {
		selectedTarget[entry] = true
	}
	for _, entry := range pending {
		key := entry.Type + ":" + strings.ToLower(entry.Name)
		if entry.RepoID != "" {
			key = entry.Type + ":" + entry.RepoID
		}
		if previous := byID[key]; previous != nil {
			previous.ListedAt = max(previous.ListedAt, entry.ListedAt)
			previous.NextAt = max(previous.NextAt, entry.NextAt)
			previous.ReadmeNextAt = max(previous.ReadmeNextAt, entry.ReadmeNextAt)
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
	completed := map[*discoveryEntry]bool{}
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
			row := &existing[i]
			if row.Type == entry.Type {
				tracked = row
			}
			if latest == nil || row.ObservedAt > latest.ObservedAt {
				latest = row
			}
			if row.Error != nil && row.AttemptedAt > now-30*60_000 {
				retryAt = max(retryAt, row.AttemptedAt+30*60_000)
			}
		}
		if tracked != nil {
			if _, e = s.store.DB.ExecContext(ctx, "UPDATE projects SET listed_at=MAX(listed_at,?) WHERE id=? AND cycle=?", entry.ListedAt, tracked.ID, tracked.Cycle); e != nil {
				return false, e
			}
			completed[entry] = true
			continue
		}
		if entry.Type == "linux.do" {
			qualification, err := s.linuxdoReadme(ctx, entry.RepoID)
			if err != nil {
				entry.ReadmeNextAt = s.readmeRetryAt()
				record("admission", entry.Type, false, safeError(err))
				continue
			}
			entry.ReadmeNextAt = 0
			if !qualification.Eligible {
				if e = s.store.SetState(readmeCacheKey(entry.RepoID), qualification); e != nil {
					return false, e
				}
				completed[entry] = true
				record("admission", entry.Type, true, "")
				continue
			}
		}
		if latest != nil && latest.ObservedAt >= slot {
			repo, e := rowRepository(*latest)
			if e != nil {
				return false, e
			}
			reuse = append(reuse, observation{repo: repo, typ: entry.Type, listedAt: ptr(entry.ListedAt), observedAt: latest.ObservedAt, metadataAt: latest.MetadataAt})
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
		due, e := s.store.DB.QueryContext(ctx, `SELECT repo_id FROM projects WHERE observed_at<? AND (error IS NULL OR attempted_at<=?) GROUP BY repo_id ORDER BY MIN(attempted_at),repo_id LIMIT 100`, slot, now-30*60_000)
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
		nodes, err := s.upstream.repositories(ctx, s.options.Token, group, true, mode)
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
					observations = append(observations, observation{repo: repo, typ: entry.Type, listedAt: ptr(entry.ListedAt), metadataAt: latestMetadata})
					completed[entry] = true
					record("admission", entry.Type, true, "")
				}
			} else {
				for _, row := range existing {
					if _, e = s.store.DB.ExecContext(ctx, "UPDATE projects SET attempted_at=?,error=? WHERE id=? AND cycle=? AND attempted_at<=?", now, message, row.ID, row.Cycle, now); e != nil {
						return false, e
					}
					record("collection", row.Type, false, message)
				}
				for _, entry := range additions {
					entry.NextAt = max(s.upstream.clock.Now()+30*60_000, s.upstream.blocked("https://api.github.com/graphql"))
					record("admission", entry.Type, false, message)
				}
			}
		}
		if e = s.store.saveObservations(ctx, observations, now, policy); e != nil {
			return false, e
		}
	}
	pending = slices.DeleteFunc(pending, func(v *discoveryEntry) bool { return completed[v] })
	changes := []string{}
	for _, kind := range []string{"collection", "admission"} {
		for _, typ := range projectTypes {
			key := kind + ":" + typ
			if results[key].Succeeded > 0 || results[key].Failed > 0 {
				changes = append(changes, key)
			}
		}
	}
	pendingChanged := jsonText(pending) != priorJSON
	if pendingChanged || len(changes) > 0 {
		tx, e := s.store.DB.BeginTx(ctx, nil)
		if e != nil {
			return false, e
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
			if e == nil && global.Succeeded+global.Failed > 0 {
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
	}
	return d.changed || pendingChanged || len(changes) > 0, nil
}

// Both sampling and submission stages use one owner for host state and persistence.
func (s *Scheduler) lockUpstream() (func() error, error) {
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
