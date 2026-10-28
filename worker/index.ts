import { cleanup, prune } from "./retention";
import { query } from "./api";
import { repositories } from "./github";
import { discover, emptyDiscovery, type DiscoveryState } from "./discovery";
import { Upstream, safeError, type Hosts } from "./upstream";
import { saveObservations, setState, state, type ProjectRow } from "./store";
import { HOUR } from "./trends";
import { type RunState } from "./contracts";
export interface Env {
  DB: D1Database;
  GITHUB_TOKEN: string;
  ALLOWED_ORIGIN: string;
}

interface PendingDiscovery {
  name: string;
  listedAt: number;
}

async function collect(env: Env, now: number, upstream: Upstream) {
  const prior = await state<RunState>(env.DB, "collection");
  const result: RunState = {
    attemptedAt: now,
    succeededAt: prior?.succeededAt ?? null,
    succeeded: 0,
    failed: 0,
    error: null,
  };
  let rows = (await env.DB.prepare("SELECT * FROM projects").all<ProjectRow>())
    .results;
  const discovery = await discover(
    upstream,
    (await state<DiscoveryState>(env.DB, "discoverySources")) ?? emptyDiscovery(),
    now,
    await state<RunState>(env.DB, "discovery"),
  );
  const known = new Set(rows.map((r) => r.name.toLowerCase()));
  const listed = new Map(
    discovery.entries.map((entry) => [entry.name.toLowerCase(), entry.listedAt]),
  );
  const updates = rows.filter((r) => listed.has(r.name.toLowerCase())).map((r) =>
    env.DB.prepare(
      "UPDATE projects SET listed_at=MAX(listed_at,?) WHERE id=? AND cycle=?",
    ).bind(listed.get(r.name.toLowerCase()), r.id, r.cycle),
  );
  if (updates.length) await env.DB.batch(updates);
  const priorPending = (await state<PendingDiscovery[]>(env.DB, "pending")) ?? [];
  const discoveries = new Map(
    priorPending.map((entry) => [entry.name.toLowerCase(), entry]),
  );
  for (const entry of discovery.entries) {
    const previous = discoveries.get(entry.name.toLowerCase());
    if (!previous || previous.listedAt < entry.listedAt)
      discoveries.set(entry.name.toLowerCase(), entry);
  }
  let pending = [...discoveries.values()].filter(
    (entry) => !known.has(entry.name.toLowerCase()),
  );
  // Persist discovery and detail progress before repository metadata collection.
  await env.DB.batch([
    setState(env.DB, "pending", pending),
    setState(env.DB, "discovery", discovery.status),
    setState(env.DB, "discoverySources", discovery.state),
  ]);
  const targets = pending.slice(0, 20);
  if (targets.length) {
    try {
      const nodes = await repositories(
        env.GITHUB_TOKEN,
        targets.map((entry) => entry.name),
        false,
        upstream,
      );
      const successful = nodes.flatMap((repo, i) =>
        repo
          ? [
              {
                repo,
                previous: rows.find((r) => r.id === repo.id),
                listedAt: targets[i].listedAt,
              },
            ]
          : [],
      );
      // Redirects / duplicate RSS names can resolve to the same stable id.
      await saveObservations(
        env.DB,
        [
          ...successful
            .reduce((unique, observation) => {
              const prior = unique.get(observation.repo.id);
              if (!prior || prior.listedAt < observation.listedAt)
                unique.set(observation.repo.id, observation);
              return unique;
            }, new Map<string, (typeof successful)[number]>())
            .values(),
        ],
        now,
      );
      pending = [
        ...pending.slice(targets.length),
        ...targets.filter((_, i) => !nodes[i]),
      ];
      result.succeeded += successful.length;
      const failed = nodes.filter((node) => !node).length;
      result.failed += failed;
      if (failed) result.error = "GitHub repository unavailable";
    } catch (error) {
      result.failed += targets.length;
      result.error = safeError(error);
      pending = [...pending.slice(targets.length), ...targets];
    }
  }
  rows = (await env.DB.prepare("SELECT * FROM projects").all<ProjectRow>())
    .results;
  const due = rows
    .filter(
      (row) => Math.floor(row.observed_at / HOUR) < Math.floor(now / HOUR),
    )
    .sort((a, b) => a.attempted_at - b.attempted_at || a.id.localeCompare(b.id))
    .slice(0, 180);
  // Two bounded upstream calls; every DB write remains fenced to the selected cycle.
  for (let start = 0; start < due.length; start += 90) {
    const group = due.slice(start, start + 90);
    let nodes: Awaited<ReturnType<typeof repositories>> = [];
    let error = "GitHub repository unavailable";
    try {
      nodes = await repositories(
        env.GITHUB_TOKEN,
        group.map((row) => row.id),
        true,
        upstream,
      );
    } catch (cause) {
      error = safeError(cause);
    }
    const byId = new Map(
      nodes.flatMap((repo) => (repo ? [[repo.id, repo] as const] : [])),
    );
    await saveObservations(
      env.DB,
      group.flatMap((previous) => {
        const repo = byId.get(previous.id);
        return repo ? [{ repo, previous, listedAt: null }] : [];
      }),
      now,
    );
    const failures = group.filter((row) => !byId.has(row.id));
    if (failures.length)
      await env.DB.batch(
        failures.map((row) =>
          env.DB.prepare(
            "UPDATE projects SET attempted_at=?,error=? WHERE id=? AND cycle=? AND attempted_at <= ?",
          ).bind(now, error, row.id, row.cycle, now),
        ),
      );
    result.succeeded += group.length - failures.length;
    result.failed += failures.length;
    if (failures.length) result.error = error;
  }
  if (result.succeeded) result.succeededAt = now;
  await env.DB.batch([
    setState(env.DB, "pending", pending),
    setState(env.DB, "collection", result),
  ]);
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
    try {
      Object.assign(upstream.hosts, (await state<Hosts>(env.DB, "upstream")) ?? {});
      await prune(env.DB, now);
      await collect(env, now, upstream);
      await cleanup(env.DB, now);
      await setState(env.DB, "scheduler", {
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
