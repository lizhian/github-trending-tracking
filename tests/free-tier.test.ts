import { expect, test, vi } from "vitest";
import { Miniflare, convertV4MiniflareOptions } from "miniflare";
import { readFileSync, readdirSync, writeFileSync } from "node:fs";
import { WINDOWS, type Point, type WindowHours } from "../worker/contracts";
import { projects, saveObservations, setState, type ProjectRow } from "../worker/store";
import { prune } from "../worker/retention";
import { DAY, HOUR, summarize, windowTrend } from "../worker/trends";
import worker from "../worker/index";
import { SOURCES } from "../worker/discovery";

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

test("1000 projects with 30 days fit the background D1 budget without parsing full histories", async () => {
  const mf = new Miniflare(convertV4MiniflareOptions({
    name: "budget", modules: true,
    script: "export default {fetch() {return new Response()}}",
    d1Databases: ["DB"], compatibilityDate: "2026-10-01",
  }));
  try {
    const db = await mf.getD1Database("DB") as unknown as D1Database;
    for (const file of readdirSync("migrations").filter((file) => file.endsWith(".sql")).sort()) {
      await db.exec(readFileSync(`migrations/${file}`, "utf8").replace(/--[^\n]*/g, "").replace(/\n/g, " "));
    }
    const now = Date.parse("2026-10-28T12:05:00Z");
    const start = now - 30 * DAY;
    const points: Point[] = Array.from({ length: 720 }, (_, i) => [start + i * HOUR, 10_000 + i]);
    const summaries = Object.fromEntries(WINDOWS.map((hours) => {
      const { points: _, ...summary } = windowTrend(points, hours, now - HOUR);
      return [hours, summary];
    }));
    await db.prepare(`WITH RECURSIVE ids(n) AS (VALUES(0) UNION ALL SELECT n+1 FROM ids WHERE n<999)
      INSERT INTO projects(id,cycle,name,topics,entered_at,listed_at,observed_at,attempted_at,stars,trends,metadata_at)
      SELECT 'R_'||n,'cycle-'||n,'acme/repo-'||n,'[]',?,?,?,?,10719,?,? FROM ids`)
      .bind(start, start, now - HOUR, now - HOUR, JSON.stringify(summaries), now - HOUR).run();
    await db.prepare(`WITH RECURSIVE ticks(n) AS (VALUES(0) UNION ALL SELECT n+1 FROM ticks WHERE n<719),
      days AS (SELECT CAST((?+n*${HOUR})/${DAY} AS INTEGER) AS day,
        json_group_object(CAST(CAST((?+n*${HOUR})/${HOUR} AS INTEGER) AS TEXT),json_array(?+n*${HOUR},10000+n)) AS hours
        FROM ticks GROUP BY day)
      INSERT INTO snapshot_days(cycle,day,hours) SELECT cycle,day,hours FROM projects CROSS JOIN days`)
      .bind(start, start, start).run();
    const rows = (await db.prepare("SELECT * FROM projects ORDER BY id LIMIT 30").all<ProjectRow>()).results;
    const meter = metered(db);
    await saveObservations(meter.db, rows.map((previous) => ({
      repo: { id: previous.id, stargazerCount: 10720 }, previous, listedAt: null,
    })), now);
    const sampling = meter.usage();
    const dueMeter = metered(db);
    await dueMeter.db.prepare("SELECT * FROM projects WHERE observed_at < ? ORDER BY attempted_at,id LIMIT 30")
      .bind(Math.floor(now / HOUR) * HOUR).all();
    const cold = metered(db);
    const allRows = (await cold.db.prepare("SELECT * FROM projects").all<ProjectRow>()).results;
    await projects(cold.db, allRows, now + 60_000);
    const status = { attemptedAt: now - HOUR, succeededAt: now - HOUR, succeeded: 1, failed: 0, error: null };
    await db.batch([
      setState(db, "discoverySources", { sources: Object.fromEntries(SOURCES.map(({ id }) => [id, { ...status, nextAt: now + DAY }])), details: [], resolved: {} }),
      setState(db, "discovery", status), setState(db, "collection", status),
      setState(db, "scheduler", status), setState(db, "upstream", {}),
      setState(db, "cleanup", { at: now, removed: 0, remaining: 1000, eligible: 0 }),
    ]);
    const scheduled = metered(db);
    vi.spyOn(Date, "now").mockReturnValue(now);
    vi.stubGlobal("fetch", async (_input: unknown, init: RequestInit) => {
      const query = JSON.parse(String(init.body)).query as string;
      const ids = JSON.parse(query.match(/ids:(\[[^\]]*\])/)![1]) as string[];
      return Response.json({ data: { nodes: ids.map((id) => ({ id, stargazerCount: 10720 })) } });
    });
    await worker.scheduled({} as ScheduledController, { DB: scheduled.db, GITHUB_TOKEN: "test", ALLOWED_ORIGIN: "https://site.test" }, {} as ExecutionContext);
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    const updated = (await db.prepare("SELECT * FROM projects WHERE id=?").bind(rows[0].id).all<ProjectRow>()).results;
    const [item] = await projects(db, updated, now);
    for (const hours of WINDOWS) {
      const { points: _, ...expected } = windowTrend([...points, [now, 10720]], hours, now);
      expect(item.trends[hours]).toEqual(expected);
      expect(item.trends[hours]).not.toHaveProperty("points");
    }
    const beforePrune = await db.prepare("SELECT hours FROM snapshot_days WHERE cycle=? ORDER BY day LIMIT 1")
      .bind(rows[0].cycle).first<{ hours: string }>();
    const cleaning = metered(db);
    await prune(cleaning.db, now + 60_000);
    expect(await db.prepare("SELECT hours FROM snapshot_days WHERE cycle=? ORDER BY day LIMIT 1")
      .bind(rows[0].cycle).first()).toEqual(beforePrune);
    expect(cleaning.usage().writes).toBe(0);
    await prune(cleaning.db, now + DAY);
    const storage = (await db.prepare("SELECT 1").all()).meta.size_after;
    const dailySampling = { reads: sampling.reads * 800, writes: sampling.writes * 800 };
    const report = { sampling, dailySampling, dueSelection: dueMeter.usage(), scheduled: scheduled.usage(), coldList: cold.usage(),
      pruning: cleaning.usage(), storageBytes: storage };
    if (process.env.FREE_TIER_BUDGET_REPORT) {
      // Local Node CPU for trend functions only; excludes D1 and Worker runtime.
      const perBatch = (build: () => unknown, iterations: number) => {
        for (let i = 0; i < 100; i++) build();
        const start = process.cpuUsage();
        for (let i = 0; i < iterations; i++) build();
        const used = process.cpuUsage(start);
        return (used.user + used.system) / 1000 / iterations * 30;
      };
      const complete: Point[] = [...points, [now, 10720]];
      const first = Object.fromEntries(WINDOWS.map((hours) => [hours, points[720 - hours]])) as Record<WindowHours, Point>;
      writeFileSync(process.env.FREE_TIER_BUDGET_REPORT, JSON.stringify({ ...report,
        localTrendCpuMsPer30: {
          fullHistory: perBatch(() => WINDOWS.map((hours) => windowTrend(complete, hours, now)), 1000),
          exactBaselines: perBatch(() => summarize(first, [now, 10720], []), 10000),
        },
      }, null, 2));
    }
    // Leave room for due selection, scheduler state, expiry and public queries.
    expect(dailySampling.reads).toBeLessThan(4_000_000);
    expect(dailySampling.writes).toBeLessThan(85_000);
    expect(sampling.calls).toBeLessThan(5);
    expect(scheduled.usage().calls).toBeLessThan(50);
    expect(storage).toBeLessThan(100_000_000);
  } finally {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    await mf.dispose();
  }
}, 60_000);
