import { cleanup, prune } from "./retention";
import { query } from "./api";
import { repositories, type Repository, type RepositoryIdentity, type RepositoryStars } from "./github";
import { discover, emptyDiscovery, type DiscoveryState } from "./discovery";
import { Upstream, safeError, type Hosts } from "./upstream";
import { samplingPolicy, saveObservations, setState, state, type ProjectRow } from "./store";
import { DAY } from "./trends";
import { samplingSlot, type SamplingPolicy } from "./schedule";
import { PROJECT_TYPES, projectId, type ProjectType, type RunState } from "./contracts";
export interface Env {
  DB: D1Database;
  GITHUB_TOKEN: string;
  ALLOWED_ORIGIN: string;
  LINUXDO_RSS_FALLBACK_URL?: string;
}

interface PendingDiscovery {
  type: ProjectType;
  name: string;
  listedAt: number;
  repoId?: string;
  nextAt?: number;
}
const discoveryKey = (entry: { type: ProjectType; name: string }) => `${entry.type}:${entry.name.toLowerCase()}`;
function repositoryFrom(row: ProjectRow): Repository {
  return {
    id: row.repo_id, nameWithOwner: row.name, description: row.description,
    primaryLanguage: row.language ? { name: row.language } : null,
    repositoryTopics: { nodes: (JSON.parse(row.topics) as string[]).map((name) => ({ topic: { name } })) },
    stargazerCount: row.stars,
  };
}

async function collect(env: Env, now: number, upstream: Upstream, policy: SamplingPolicy) {
  const prior = await state<RunState>(env.DB, "collection");
  const priorDiscovery = await state<RunState>(env.DB, "discovery");
  const priorByType = Object.fromEntries(await Promise.all(PROJECT_TYPES.map(async (type) => [
    type, await state<RunState>(env.DB, `discovery:${type}`) ?? (type === "github" ? priorDiscovery : null),
  ])));
  const discovery = await discover(
    upstream, (await state<DiscoveryState>(env.DB, "discoverySources")) ?? emptyDiscovery(),
    now, priorDiscovery, policy, { priorByType, linuxdoFallbackUrl: env.LINUXDO_RSS_FALLBACK_URL },
  );
  const priorPending = (await state<PendingDiscovery[]>(env.DB, "pending")) ?? [];
  const discoveries = new Map(priorPending.map((entry) => {
    const typed = { ...entry, type: entry.type ?? "github" as const };
    return [discoveryKey(typed), typed];
  }));
  for (const entry of discovery.entries) {
    const key = discoveryKey(entry);
    const previous = discoveries.get(key);
    if (!previous || previous.listedAt < entry.listedAt) discoveries.set(key, { ...previous, ...entry });
  }
  let pending = [...discoveries.values()];
  // Full rows are needed only for new discoveries; ordinary ticks read due repositories.
  const rows = pending.length ? (await env.DB.prepare("SELECT * FROM projects").all<ProjectRow>()).results : [];
  const listedUpdates: D1PreparedStatement[] = [];
  pending = pending.filter((entry) => {
    const matching = rows.find((row) => row.name.toLowerCase() === entry.name.toLowerCase());
    entry.repoId ??= matching?.repo_id;
    const tracked = rows.find((row) => row.type === entry.type && row.repo_id === entry.repoId);
    if (!tracked) return true;
    listedUpdates.push(env.DB.prepare("UPDATE projects SET listed_at=MAX(listed_at,?) WHERE id=? AND cycle=?")
      .bind(entry.listedAt, tracked.id, tracked.cycle));
    return false;
  });
  if (listedUpdates.length) await env.DB.batch(listedUpdates);
  if (discovery.changed) await env.DB.batch([
    setState(env.DB, "pending", pending), setState(env.DB, "discovery", discovery.status),
    setState(env.DB, "discoverySources", discovery.state),
    ...PROJECT_TYPES.map((type) => setState(env.DB, `discovery:${type}`, discovery.byType[type])),
  ]);
  const results = Object.fromEntries(await Promise.all(PROJECT_TYPES.map(async (type) => {
    const previous = await state<RunState>(env.DB, `collection:${type}`) ?? (type === "github" ? prior : null);
    return [type, { attemptedAt: now, succeededAt: previous?.succeededAt ?? null,
      succeeded: 0, failed: 0, error: null }];
  }))) as Record<ProjectType, RunState>;
  const record = (type: ProjectType, success: boolean, error: string | null = null) => {
    const result = results[type];
    if (success) { result.succeeded++; result.succeededAt = now; }
    else { result.failed++; result.error = error; }
  };
  const githubReady = () => upstream.blockedUntil("https://api.github.com/graphql") <= upstream.clock.now();
  const slot = samplingSlot(now, policy);
  // Resolve identity separately: an old name / redirect must not fetch stars again
  // before we know whether this stable repository has already succeeded this round.
  const targets = pending.filter((entry) => (entry.nextAt ?? 0) <= now).slice(0, 20);
  const unresolved = [...new Set(targets.filter((entry) => !entry.repoId).map((entry) => entry.name.toLowerCase()))];
  if (unresolved.length && githubReady()) {
    let identities: (RepositoryIdentity | null)[] = [];
    let error = "GitHub repository unavailable";
    try { identities = await repositories(env.GITHUB_TOKEN, unresolved, false, upstream, "identity"); }
    catch (cause) { error = safeError(cause); }
    for (const entry of targets.filter((entry) => !entry.repoId)) {
      const identity = identities[unresolved.indexOf(entry.name.toLowerCase())];
      if (identity) {
        entry.repoId = identity.id;
        const renamed = rows.filter((row) => row.repo_id === identity.id && row.name !== identity.nameWithOwner);
        if (renamed.length) {
          await env.DB.prepare("UPDATE projects SET name=? WHERE repo_id=?").bind(identity.nameWithOwner, identity.id).run();
          for (const row of renamed) row.name = identity.nameWithOwner;
        }
      } else {
        entry.nextAt = Math.max(upstream.clock.now() + 30 * 60_000, upstream.blockedUntil("https://api.github.com/graphql"));
        record(entry.type, false, error);
      }
    }
  }
  const canonical = new Map<string, PendingDiscovery>();
  for (const entry of pending) {
    const key = entry.repoId ? projectId(entry.type, entry.repoId) : discoveryKey(entry);
    const previous = canonical.get(key);
    if (previous) {
      previous.listedAt = Math.max(previous.listedAt, entry.listedAt);
      previous.nextAt = Math.max(previous.nextAt ?? 0, entry.nextAt ?? 0);
    } else canonical.set(key, entry);
  }
  pending = [...canonical.values()];
  // A repository can fail admission before either type has a projects row.
  // Keep its retry time shared even when the second type is discovered later.
  const retryByRepo = new Map<string, number>();
  for (const entry of pending) {
    if (entry.repoId) retryByRepo.set(entry.repoId, Math.max(retryByRepo.get(entry.repoId) ?? 0, entry.nextAt ?? 0));
  }
  for (const entry of pending) {
    const retryAt = entry.repoId && retryByRepo.get(entry.repoId);
    if (retryAt) entry.nextAt = retryAt;
  }
  const completed = new Set<PendingDiscovery>();
  const ready = pending.filter((entry) => targets.includes(entry) && entry.repoId && (entry.nextAt ?? 0) <= now);
  const reuse: Parameters<typeof saveObservations>[1] = [];
  for (const entry of ready) {
    const tracked = rows.find((row) => row.id === projectId(entry.type, entry.repoId!));
    if (tracked) {
      await env.DB.prepare("UPDATE projects SET listed_at=MAX(listed_at,?) WHERE id=? AND cycle=?")
        .bind(entry.listedAt, tracked.id, tracked.cycle).run();
      completed.add(entry);
      continue;
    }
    const existing = rows.filter((row) => row.repo_id === entry.repoId);
    const latest = existing.sort((a, b) => b.observed_at - a.observed_at)[0];
    if (latest && latest.observed_at >= slot) {
      reuse.push({ repo: repositoryFrom(latest), type: entry.type, listedAt: entry.listedAt,
        observedAt: latest.observed_at, metadataAt: latest.metadata_at });
      completed.add(entry);
      record(entry.type, true);
    } else if (existing.some((row) => row.error && row.attempted_at > now - 30 * 60_000)) {
      entry.nextAt = Math.max(...existing.map((row) => row.attempted_at)) + 30 * 60_000;
    }
  }
  await saveObservations(env.DB, reuse, now, policy);
  // Group before limiting, then load every type belonging to the selected ids.
  // This prevents a second type landing in a later batch and triggering a duplicate.
  const due = githubReady() ? (await env.DB.prepare(
    `SELECT repo_id FROM projects WHERE observed_at < ? AND (error IS NULL OR attempted_at <= ?)
     GROUP BY repo_id ORDER BY MIN(attempted_at),repo_id LIMIT 100`,
  ).bind(slot, now - 30 * 60_000).all<{ repo_id: string }>()).results : [];
  const newEntries = ready.filter((entry) => !completed.has(entry) && (entry.nextAt ?? 0) <= now);
  const ids = githubReady() ? [...new Set([...newEntries.map((entry) => entry.repoId!), ...due.map((row) => row.repo_id)])].slice(0, 100) : [];
  const selected: ProjectRow[] = [];
  for (let offset = 0; offset < ids.length; offset += 90) {
    const group = ids.slice(offset, offset + 90);
    selected.push(...(await env.DB.prepare(`SELECT * FROM projects WHERE repo_id IN (${group.map(() => "?").join(",")})`)
      .bind(...group).all<ProjectRow>()).results);
  }
  const needsMetadata = (id: string) => {
    const existing = selected.filter((row) => row.repo_id === id);
    return !existing.length || Math.max(...existing.map((row) => row.metadata_at)) <= now - DAY;
  };
  for (const metadata of [true, false] as const) {
    if (!githubReady()) break;
    const group = ids.filter((id) => needsMetadata(id) === metadata);
    if (!group.length) continue;
    let nodes: (Repository | RepositoryStars | null)[] = [];
    let error = "GitHub repository unavailable";
    try {
      nodes = metadata ? await repositories(env.GITHUB_TOKEN, group, true, upstream)
        : await repositories(env.GITHUB_TOKEN, group, true, upstream, false);
    } catch (cause) { error = safeError(cause); }
    const byId = new Map(nodes.flatMap((repo) => repo ? [[repo.id, repo] as const] : []));
    const observations: Parameters<typeof saveObservations>[1] = [];
    const failures: D1PreparedStatement[] = [];
    for (const id of group) {
      const existing = selected.filter((row) => row.repo_id === id);
      const additions = newEntries.filter((entry) => entry.repoId === id);
      const repo = byId.get(id);
      if (repo) {
        const latestMetadata = existing.sort((a, b) => b.metadata_at - a.metadata_at)[0];
        const shared = "nameWithOwner" in repo ? repo : { ...repositoryFrom(latestMetadata), ...repo };
        const metadataAt = metadata ? now : latestMetadata.metadata_at;
        for (const previous of existing) {
          observations.push({ repo: shared, previous, listedAt: null, metadataAt });
          record(previous.type, true);
        }
        for (const entry of additions) {
          observations.push({ repo: shared, type: entry.type, listedAt: entry.listedAt, metadataAt });
          completed.add(entry);
          record(entry.type, true);
        }
      } else {
        for (const row of existing) {
          failures.push(env.DB.prepare("UPDATE projects SET attempted_at=?,error=? WHERE id=? AND cycle=? AND attempted_at <= ?")
            .bind(now, error, row.id, row.cycle, now));
          record(row.type, false, error);
        }
        for (const entry of additions) {
          entry.nextAt = Math.max(upstream.clock.now() + 30 * 60_000, upstream.blockedUntil("https://api.github.com/graphql"));
          record(entry.type, false, error);
        }
      }
    }
    await saveObservations(env.DB, observations, now, policy);
    if (failures.length) await env.DB.batch(failures);
  }
  pending = pending.filter((entry) => !completed.has(entry));
  const changes = PROJECT_TYPES.filter((type) => results[type].succeeded || results[type].failed);
  const pendingChanged = JSON.stringify(pending) !== JSON.stringify(priorPending);
  if (pendingChanged || changes.length) await env.DB.batch([
    ...(pendingChanged ? [setState(env.DB, "pending", pending)] : []),
    ...changes.map((type) => setState(env.DB, `collection:${type}`, results[type])),
    ...(changes.length ? [setState(env.DB, "collection", {
      attemptedAt: now, succeededAt: changes.some((type) => results[type].succeeded) ? now : prior?.succeededAt ?? null,
      succeeded: changes.reduce((sum, type) => sum + results[type].succeeded, 0),
      failed: changes.reduce((sum, type) => sum + results[type].failed, 0),
      error: changes.map((type) => results[type].error).find(Boolean) ?? null,
    })] : []),
  ]);
  return discovery.changed || pendingChanged || changes.length > 0;
}

export default {
  async scheduled(
    _controller: ScheduledController,
    env: Env,
    _ctx: ExecutionContext,
  ) {
    const now = Date.now();
    const lease = crypto.randomUUID();
    const acquired = await env.DB.prepare(
      `INSERT INTO system_state(key,value) VALUES('lease',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value WHERE json_extract(value,'$.until') < ? RETURNING key`,
    )
      .bind(JSON.stringify({ lease, until: now + 9 * 60_000 }), now)
      .first();
    if (!acquired) return;
    const upstream = new Upstream({});
    let previousHosts = "{}";
    try {
      Object.assign(upstream.hosts, (await state<Hosts>(env.DB, "upstream")) ?? {});
      previousHosts = JSON.stringify(upstream.hosts);
      const policy = (await samplingPolicy(env.DB, now))!;
      const pruned = await prune(env.DB, now);
      const collected = await collect(env, now, upstream, policy);
      const cleaned = await cleanup(env.DB, now, policy);
      if (collected || cleaned || pruned.meta.rows_written) await setState(env.DB, "scheduler", {
        attemptedAt: now,
        succeededAt: now,
        succeeded: 1,
        failed: 0,
        error: null,
      }).run();
    } catch {
      await setState(env.DB, "scheduler", {
        attemptedAt: now,
        succeededAt: null,
        succeeded: 0,
        failed: 1,
        error: "采集或数据库任务失败",
      }).run();
      throw new Error("Scheduled tracking failed");
    } finally {
      if (JSON.stringify(upstream.hosts) !== previousHosts)
        await setState(env.DB, "upstream", upstream.hosts).run();
      await env.DB.prepare(
        "DELETE FROM system_state WHERE key='lease' AND json_extract(value,'$.lease')=?",
      )
        .bind(lease)
        .run();
    }
  },
  async fetch(request: Request, env: Env, _ctx: ExecutionContext) {
    return query(request, env);
  },
};
