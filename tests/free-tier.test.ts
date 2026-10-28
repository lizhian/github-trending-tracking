import { expect, test, vi } from "vitest";
import { Miniflare, convertV4MiniflareOptions } from "miniflare";
import { readFileSync, readdirSync, writeFileSync } from "node:fs";
import { WINDOWS, type Point } from "../worker/contracts";
import { setState } from "../worker/store";
import { prune } from "../worker/retention";
import { DAY, HOUR, windowTrend } from "../worker/trends";
import worker from "../worker/index";
import { SOURCES } from "../worker/discovery";
import { latestSlot, nextSlot, type SamplingPolicy } from "../worker/schedule";

// Measure SQLite's reported rows, including indexes and JSON virtual tables.
function metered(db: D1Database) {
  let reads = 0, writes = 0, calls = 0;
  const originals = new WeakMap<object, D1PreparedStatement>();
  const count = <T extends D1Result>(result: T) => {
    reads += result.meta.rows_read;
    writes += result.meta.rows_written;
    return result;
  };
  const wrap = (statement: D1PreparedStatement): D1PreparedStatement => {
    const wrapped = {
      bind: (...values: unknown[]) => wrap(statement.bind(...values)),
      all: async () => { calls++; return count(await statement.all()); },
      run: async () => { calls++; return count(await statement.run()); },
      first: async (column?: string) => {
        calls++;
        const result = count(await statement.all<Record<string, unknown>>());
        const first = result.results[0];
        return first ? column ? first[column] : first : null;
      },
    } as unknown as D1PreparedStatement;
    originals.set(wrapped, statement);
    return wrapped;
  };
  return {
    db: {
      prepare: (sql: string) => wrap(db.prepare(sql)),
      batch: async (statements: D1PreparedStatement[]) => {
        calls++;
        return (await db.batch(statements.map((statement) => originals.get(statement)!))).map(count);
      },
    } as D1Database,
    usage: () => ({ reads, writes, calls }),
  };
}

test.each([true, false])("bounds two 500-project types with 30 days of history (shared repositories: %s)", async (shared) => {
  const mf = new Miniflare(convertV4MiniflareOptions({
    name: "budget", modules: true, script: "export default {fetch() {return new Response()}}",
    d1Databases: ["DB"], compatibilityDate: "2026-10-01",
  }));
  try {
    const db = await mf.getD1Database("DB") as unknown as D1Database;
    for (const file of readdirSync("migrations").filter((file) => file.endsWith(".sql")).sort()) {
      await db.exec(readFileSync(`migrations/${file}`, "utf8").replace(/--[^\n]*/g, "").replace(/\n/g, " "));
    }
    let now = Date.parse("2026-10-28T01:05:00Z"); // Beijing 09:05
    const start = now - 30 * DAY;
    const policy: SamplingPolicy = { version: 2, effectiveAt: start - DAY };
    await setState(db, "sampling_policy", policy).run();
    const points: Point[] = [];
    for (let at = latestSlot(start); at < latestSlot(now); at = nextSlot(at))
      points.push([at + 5 * 60_000, 10_000 + points.length]);
    expect(points).toHaveLength(270);
    const [previousAt, previousStars] = points.at(-1)!;
    const stars = previousStars + 1;
    const summaries = Object.fromEntries(WINDOWS.map((hours) => {
      const { points: _, ...summary } = windowTrend(points, hours, previousAt, undefined, policy);
      return [hours, summary];
    }));
    await db.prepare(`WITH RECURSIVE ids(n) AS (VALUES(0) UNION ALL SELECT n+1 FROM ids WHERE n<499),
      types(type) AS (VALUES('github'),('linux.do')),
      entries AS (SELECT type,n, 'R_' || (n + CASE WHEN type='github' OR ? THEN 0 ELSE 500 END) AS repo FROM ids CROSS JOIN types)
      INSERT INTO projects(id,type,repo_id,cycle,name,topics,entered_at,listed_at,observed_at,attempted_at,stars,trends,metadata_at)
      SELECT type||':'||repo,type,repo,type||'-'||n,'acme/'||repo,'[]',?,?,?,?,?,?,? FROM entries`)
      .bind(shared ? 1 : 0, start, start, previousAt, previousAt, previousStars, JSON.stringify(summaries), previousAt).run();
    await db.prepare(`WITH days AS (SELECT CAST(json_extract(value,'$[0]')/${DAY} AS INTEGER) AS day,
        json_group_object(CAST(CAST(json_extract(value,'$[0]')/${HOUR} AS INTEGER) AS TEXT),json(value)) AS hours
        FROM json_each(?) GROUP BY day)
      INSERT INTO snapshot_days(cycle,day,hours) SELECT cycle,day,hours FROM projects CROSS JOIN days`)
      .bind(JSON.stringify(points)).run();
    const status = { attemptedAt: now - HOUR, succeededAt: now - HOUR, succeeded: 1, failed: 0, error: null };
    await db.batch([
      setState(db, "discoverySources", { scheduleVersion: 2, sources: Object.fromEntries(SOURCES.map(({ id }) => [id, { ...status, nextAt: now + DAY }])), details: [], resolved: {} }),
      setState(db, "discovery", status), setState(db, "collection", status),
      setState(db, "scheduler", status), setState(db, "upstream", {}),
      ...["github", "linux.do"].map((type) => setState(db, `cleanup:${type}`, { at: now, removed: 0, remaining: 500, eligible: 0 })),
    ]);
    const requestedIds: string[][] = [];
    vi.spyOn(Date, "now").mockImplementation(() => now);
    vi.stubGlobal("fetch", async (_input: unknown, init: RequestInit) => {
      const query = JSON.parse(String(init.body)).query as string;
      const ids = JSON.parse(query.match(/ids:(\[[^\]]*\])/)![1]) as string[];
      requestedIds.push(ids);
      return Response.json({ data: { nodes: ids.map((id) => ({ id, stargazerCount: stars })) } });
    });
    const scheduled = metered(db);
    const env = { DB: scheduled.db, GITHUB_TOKEN: "test", ALLOWED_ORIGIN: "https://site.test" };
    await worker.scheduled({} as ScheduledController, env, {} as ExecutionContext);
    const batch = scheduled.usage();
    expect(requestedIds).toHaveLength(1);
    expect(requestedIds[0]).toHaveLength(100);
    expect(await db.prepare("SELECT COUNT(*) AS n FROM projects WHERE observed_at=?").bind(now).first("n")).toBe(shared ? 200 : 100);
    now += 60_000;
    await worker.scheduled({} as ScheduledController, env, {} as ExecutionContext);
    expect(requestedIds).toHaveLength(2);
    expect(new Set(requestedIds.flat()).size).toBe(200); // No repeated repo across ticks / type rows.
    const cold = metered(db);
    const result = await worker.fetch(new Request("https://worker.test/api/projects?type=linux.do"), { ...env, DB: cold.db }, {} as ExecutionContext);
    expect(await result.json()).toMatchObject({ total: 500, summary: { tracked: 500 } });
    const id = requestedIds[0][0];
    const history = await worker.fetch(new Request(`https://worker.test/api/projects/github:${id}/history?window=720`), env, {} as ExecutionContext);
    expect(await history.json()).toMatchObject({ observedAt: now - 60_000, trend: { state: "complete" } });
    const cleaning = metered(db);
    await prune(cleaning.db, now);
    expect(cleaning.usage().writes).toBe(0);
    await prune(cleaning.db, now + DAY);
    expect(cleaning.usage().writes).toBe(1000);
    const storage = (await db.prepare("SELECT 1").all()).meta.size_after;
    const batchesPerDay = (shared ? 500 : 1000) * 9 / 100;
    const dailyScheduled = { reads: batch.reads * batchesPerDay, writes: batch.writes * batchesPerDay };
    if (process.env.FREE_TIER_BUDGET_REPORT) writeFileSync(`${process.env.FREE_TIER_BUDGET_REPORT}.${shared ? "shared" : "distinct"}.json`, JSON.stringify({
      projects: 1000, repositories: shared ? 500 : 1000, scheduledBatch: batch, dailyScheduled,
      coldLinuxList: cold.usage(), pruning: cleaning.usage(), storageBytes: storage,
    }, null, 2));
    // Includes steady scheduler state, but not discovery, retries, admission, eviction or user traffic.
    expect(dailyScheduled.reads).toBeLessThan(2_000_000);
    expect(dailyScheduled.writes).toBeLessThan(45_000);
    expect(batch.calls).toBeLessThan(50);
    expect(storage).toBeLessThan(100_000_000);
  } finally {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    await mf.dispose();
  }
}, 60_000);
