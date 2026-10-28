import { afterAll, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { Miniflare, convertV4MiniflareOptions } from "miniflare";
import { readFileSync, readdirSync } from "node:fs";
import worker, { type Env } from "../worker/index";
import { upstreamClock } from "../worker/upstream";
import { SOURCES } from "../worker/discovery";
import { saveObservations, setState, type ProjectRow } from "../worker/store";
import { DAY, HOUR, type SamplingPolicy } from "../worker/schedule";

const mf = new Miniflare(
  convertV4MiniflareOptions({
    name: "test",
    modules: true,
    script: "export default { fetch() { return new Response() } }",
    d1Databases: ["DB"],
    compatibilityDate: "2026-10-01",
  }),
);
let env: Env;
let now = Date.parse("2026-10-01T00:05:00Z");
let names = ["acme/alpha", "acme/beta"];
let stars = [100, 200];
let fail = false;
let rssNames: string[] | null = null;
let failedIds = new Set<string>();
let rssFailure = false;
let sourceResponses = new Map<string, () => Response>();
const external = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
  const custom = sourceResponses.get(String(input));
  if (custom) return custom();
  if (String(input).includes("raw.githubusercontent.com"))
    return new Response("");
  if (
    String(input).includes("rsshub") &&
    !String(input).includes("/github/trending/daily/")
  )
    return new Response("<rss><channel></channel></rss>");
  if (String(input).includes("rsshub") && rssFailure)
    return new Response("test-secret", { status: 503 });
  if (String(input).includes("rsshub"))
    return new Response(
      `<rss><channel>${(rssNames ?? names).map((name) => `<item><link>https://github.com/${name}</link><description>See https://github.com/unrelated/linked</description></item>`).join("")}</channel></rss>`,
    );
  const query = JSON.parse(String(init?.body)).query as string;
  const nodes = names.map((name, i) => ({
    id: `R_${i}`,
    nameWithOwner: name,
    description: `Useful ${name}`,
    primaryLanguage: { name: i ? "Rust" : "TypeScript" },
    repositoryTopics: { nodes: [{ topic: { name: "tools" } }] },
    stargazerCount: stars[i],
  }));
  if (fail) return new Response("unavailable", { status: 503 });
  let selected = query.includes("nodes(")
    ? (JSON.parse(query.match(/ids:(\[[^\]]*\])/)![1]) as string[]).map(
        (id) => nodes.find((node) => node.id === id) ?? null,
      )
    : [...query.matchAll(/repository\(owner:("[^"]*"),name:("[^"]*")/g)].map(
        (match) =>
          nodes.find(
            (node) =>
              node.nameWithOwner ===
              `${JSON.parse(match[1])}/${JSON.parse(match[2])}`,
          ) ?? null,
      );
  selected = selected.map((node) =>
    node && !failedIds.has(node.id) ? node : null,
  );
  return Response.json({
    data: query.includes("nodes(")
      ? { nodes: selected }
      : Object.fromEntries(selected.map((node, i) => [`r${i}`, node])),
  });
});
async function run(at = now) {
  now = at;
  vi.spyOn(Date, "now").mockReturnValue(now);
  await worker.scheduled(
    { scheduledTime: now } as ScheduledController,
    env,
    {} as ExecutionContext,
  );
}
// Prepare large fixtures directly; scenarios still exercise scheduling and public queries.
async function seedProjects(at: number, from = 0) {
  for (let offset = from; offset < names.length; offset += 100) {
    await saveObservations(env.DB, names.slice(offset, offset + 100).map((nameWithOwner, i) => ({
      repo: { id: `R_${offset + i}`, nameWithOwner, description: null, primaryLanguage: null,
        repositoryTopics: { nodes: [] }, stargazerCount: 100 }, listedAt: at,
    })), at);
  }
}
async function api(path = "/api/projects") {
  return worker.fetch(
    new Request(`https://worker.test${path}`),
    env,
    {} as ExecutionContext,
  );
}
async function list(query = "") {
  return (await api(`/api/projects${query}`)).json() as Promise<any>;
}
beforeAll(async () => {
  env = {
    DB: (await mf.getD1Database("DB")) as unknown as D1Database,
    GITHUB_TOKEN: "test-secret",
    ALLOWED_ORIGIN: "https://site.test",
  };
  for (const file of readdirSync("migrations")
    .filter((file) => file.endsWith(".sql"))
    .sort()) {
    await env.DB.exec(
      readFileSync(`migrations/${file}`, "utf8")
        .replace(/--[^\n]*/g, "")
        .replace(/\n/g, " "),
    );
  }
});
beforeEach(async () => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.stubGlobal("fetch", external);
  vi.spyOn(upstreamClock, "sleep").mockResolvedValue();
  external.mockClear();
  await env.DB.batch(
    [
      "DELETE FROM snapshot_days",
      "DELETE FROM projects",
      "DELETE FROM system_state",
    ].map((sql) => env.DB.prepare(sql)),
  );
  now = Date.parse("2026-10-01T00:05:00Z");
  names = ["acme/alpha", "acme/beta"];
  stars = [100, 200];
  fail = false;
  rssNames = null;
  failedIds = new Set();
  rssFailure = false;
  sourceResponses = new Map();
  await setState(env.DB, "sampling_policy", { version: 2, effectiveAt: now - 30 * DAY }).run();
});
afterAll(async () => {
  vi.unstubAllGlobals();
  await mf.dispose();
});

test("completes 500 projects in five scheduled batches and leaves completed/idle rounds untouched", async () => {
  const start = Date.parse("2026-10-02T01:00:00Z"); // Beijing 09:00
  const policy: SamplingPolicy = { version: 2, effectiveAt: start - DAY };
  await setState(env.DB, "sampling_policy", policy).run();
  names = Array.from({ length: 500 }, (_, i) => `acme/round-${i}`);
  stars = names.map(() => 120);
  await seedProjects(start - 4 * HOUR + 4 * 60_000);
  const success = { attemptedAt: start - HOUR, succeededAt: start - HOUR, succeeded: 1, failed: 0, error: null, nextAt: start + DAY };
  await setState(env.DB, "discoverySources", { scheduleVersion: 2,
    sources: Object.fromEntries(SOURCES.map(({ id }) => [id, success])), details: [], resolved: {} }).run();
  external.mockClear();
  for (let i = 0; i < 5; i++) await run(start + i * 60_000);
  const requests = external.mock.calls.filter(([input]) => String(input).includes("api.github.com"));
  expect(requests).toHaveLength(5);
  const ids = requests.flatMap(([, init]) => {
    const query = JSON.parse(String(init?.body)).query as string;
    const group = JSON.parse(query.match(/ids:(\[[^\]]*\])/)![1]) as string[];
    expect(group).toHaveLength(100);
    return group;
  });
  expect(new Set(ids).size).toBe(500);
  expect(await env.DB.prepare("SELECT COUNT(*) AS n FROM projects WHERE observed_at>=?").bind(start).first("n")).toBe(500);
  expect(await list()).toMatchObject({ total: 500, pageSize: 100, pages: 5 });
  const prior = await env.DB.prepare("SELECT key,value FROM system_state WHERE key IN ('collection','scheduler') ORDER BY key").all();
  await run(start + 9 * 60_000);
  expect(external.mock.calls.filter(([input]) => String(input).includes("api.github.com"))).toHaveLength(5);
  expect((await env.DB.prepare("SELECT key,value FROM system_state WHERE key IN ('collection','scheduler') ORDER BY key").all()).results).toEqual(prior.results);
});

test("initializes policy once, retries incomplete rounds, catches up only current observations and samples new entries immediately", async () => {
  const start = Date.parse("2026-10-02T01:05:00Z"); // Beijing 09:05
  await env.DB.prepare("DELETE FROM system_state WHERE key='sampling_policy'").run();
  await run(start);
  const firstPolicy = await env.DB.prepare("SELECT value FROM system_state WHERE key='sampling_policy'").first<string>("value");
  expect(JSON.parse(firstPolicy!)).toEqual({ version: 2, effectiveAt: start });
  failedIds.add("R_0");
  stars = [110, 210];
  await run(start + 2 * HOUR);
  const beta = await env.DB.prepare("SELECT observed_at FROM projects WHERE id='R_1'").first();
  failedIds.clear();
  const failedAttempt = await env.DB.prepare("SELECT attempted_at FROM projects WHERE id='R_0'").first();
  const requestCount = external.mock.calls.length;
  await run(start + 2 * HOUR + 60_000);
  expect(external.mock.calls.length).toBe(requestCount);
  expect(await env.DB.prepare("SELECT attempted_at FROM projects WHERE id='R_0'").first()).toEqual(failedAttempt);
  await run(start + 2 * HOUR + 31 * 60_000);
  expect(await env.DB.prepare("SELECT observed_at FROM projects WHERE id='R_1'").first()).toEqual(beta);
  expect(((await (await api("/api/projects/R_0/history?window=24")).json()) as any).gaps).toEqual([]);
  stars[0] = 105;
  await run(start + 8 * HOUR); // 17:05, no synthetic 13:00 or 15:00 observations.
  const history = await (await api("/api/projects/R_0/history?window=24")).json() as any;
  expect(history.points.map(([at]: number[]) => at)).toEqual([start, start + 2 * HOUR + 31 * 60_000, start + 8 * HOUR]);
  expect(history.gaps).toEqual([[start + 2 * HOUR + 31 * 60_000, start + 8 * HOUR]]);
  expect((await list()).items.find((item: any) => item.id === "R_0").trends[24].gaps).toEqual(history.gaps);
  fail = true;
  await run(start + 10 * HOUR); // 19:05
  expect((await list()).items.find((item: any) => item.id === "R_0")).toMatchObject({
    stars: 105, observedAt: start + 8 * HOUR, error: "GitHub HTTP 503",
  });
  expect(JSON.stringify(await list())).not.toContain("test-secret");
  const calls = external.mock.calls.length;
  const attempt = await env.DB.prepare("SELECT attempted_at FROM projects WHERE id='R_0'").first();
  await run(start + 10 * HOUR + 60_000);
  expect(external.mock.calls.length).toBe(calls);
  expect(await env.DB.prepare("SELECT attempted_at FROM projects WHERE id='R_0'").first()).toEqual(attempt);
  fail = false;
  await run(start + 10 * HOUR + 31 * 60_000);
  expect((await list()).items.every((item: any) => !item.error)).toBe(true);
  names.push("acme/new-between-rounds");
  stars.push(500);
  await setState(env.DB, "pending", [{ name: names[2], listedAt: start + 11 * HOUR }]).run();
  await run(start + 11 * HOUR); // 20:05, immediate first snapshot.
  expect((await list()).items.find((item: any) => item.id === "R_2")).toMatchObject({ enteredAt: now, observedAt: now, stars: 500 });
  expect(await env.DB.prepare("SELECT value FROM system_state WHERE key='sampling_policy'").first("value")).toBe(firstPolicy);
});

test("preserves legacy hourly gaps while subsequent planned intervals agree in list and history", async () => {
  const start = Date.parse("2026-10-02T00:05:00Z"); // Beijing 08:05, legacy policy.
  const policy: SamplingPolicy = { version: 2, effectiveAt: start + 2 * HOUR + 25 * 60_000 };
  await setState(env.DB, "sampling_policy", policy).run();
  await run(start);
  stars[0] = 130;
  await run(start + 2 * HOUR); // missing legacy 09:00
  await run(policy.effectiveAt);
  await run(start + 3 * HOUR); // 11:05
  stars[0] = 125;
  await run(start + 5 * HOUR); // 13:05, normal 2h interval.
  const history = await (await api("/api/projects/R_0/history?window=24")).json() as any;
  const summary = (await list()).items.find((item: any) => item.id === "R_0").trends[24];
  expect(history.gaps).toEqual([[start, start + 2 * HOUR]]);
  const { points: _, ...expected } = history.trend;
  expect(summary).toEqual(expected);
});

test("deduplicates renamed repositories and keeps successful samples despite source and repository failures", async () => {
  const start = now;
  await run();
  names[0] = "acme/renamed";
  stars[0] = 125;
  await run(start + HOUR);
  let result = await list();
  expect(result.total).toBe(2);
  expect(result.items[0]).toMatchObject({ id: "R_0", name: names[0], enteredAt: start, stars: 125 });
  failedIds.add("R_1");
  rssFailure = true;
  stars[0] = 140;
  await run(start + 3 * HOUR);
  result = await list();
  expect(result.items.find((p: any) => p.id === "R_0").stars).toBe(140);
  expect(result.items.find((p: any) => p.id === "R_1")).toMatchObject({
    stars: 200, error: "GitHub repository unavailable",
  });
  expect(result.status.discovery.failed).toBe(1);
  expect(JSON.stringify(result)).not.toContain("test-secret");
});

test("uses actual rolling observations, expiry, tolerance and partial periods consistently in list and history", async () => {
  const start = now;
  await run();
  stars = [150, 190];
  await run(start + 23 * 3_600_000);
  let result = await list();
  expect(result.items[0].trends["24"]).toMatchObject({
    delta: 50,
    state: "complete",
    start,
    end: start + 23 * 3_600_000,
  });
  expect(result.items[0].trends["72"]).toMatchObject({
    delta: 50,
    state: "partial",
  });
  stars = [140, 180];
  await run(start + 25 * 3_600_000);
  const alpha = (await list()).items.find((p: any) => p.id === "R_0");
  expect(alpha.trends["24"]).toMatchObject({ delta: -10, state: "partial" });
  expect(alpha.trends["720"].delta).toBe(40);
  const history = (await (
    await api("/api/projects/R_0/history?window=72")
  ).json()) as any;
  expect(history.trend.delta).toBe(40);
  expect(history.points).toHaveLength(3);
  stars = [130, 170];
  await run(start + 30 * 24 * 3_600_000);
  now += 60_000;
  vi.spyOn(Date, "now").mockReturnValue(now);
  expect(
    (await list()).items.find((p: any) => p.id === "R_0").trends["720"].delta,
  ).toBe(-20);
  expect(
    ((await (await api("/api/projects/R_0/history?window=720")).json()) as any)
      .trend.delta,
  ).toBe(-20);
  await run(start + 31 * 24 * 3_600_000);
  result = await list();
  expect(result.items[0].trends["720"]).toMatchObject({
    delta: -10,
    state: "complete",
  });
  const retained = (await (
    await api("/api/projects/R_0/history?window=720")
  ).json()) as any;
  expect(retained.points).toHaveLength(3);
  now = start + 61 * 24 * 3_600_000;
  vi.spyOn(Date, "now").mockReturnValue(now);
  expect((await list()).items[0].trends["720"]).toMatchObject({
    delta: null,
    state: "accumulating",
  });
});

test("samples only stars between daily metadata refreshes and keeps same-hour gap replacements exact", async () => {
  const start = now;
  await run();
  rssNames = [];
  external.mockClear();
  stars[0] = 140;
  await run(start + 3 * 3_600_000);
  const hourly = external.mock.calls.filter(([input]) =>
    String(input).includes("api.github.com"),
  );
  expect(hourly).toHaveLength(1);
  expect(JSON.parse(String(hourly[0][1]?.body)).query).not.toContain(
    "repositoryTopics",
  );
  const previous = await env.DB.prepare(
    "SELECT * FROM projects WHERE id='R_0'",
  ).first<ProjectRow>();
  now += 600_000;
  await saveObservations(
    env.DB,
    [
      {
        repo: { id: "R_0", stargazerCount: 135 },
        previous: previous!,
        listedAt: null,
      },
    ],
    now,
  );
  // An older result cannot replace a newer point in the same tracking cycle.
  await saveObservations(
    env.DB,
    [
      {
        repo: { id: "R_0", stargazerCount: 999 },
        previous: previous!,
        listedAt: null,
      },
    ],
    now - 60_000,
  );
  vi.spyOn(Date, "now").mockReturnValue(now);
  const item = (await list()).items.find((item: any) => item.id === "R_0");
  const history = (await (
    await api("/api/projects/R_0/history?window=720")
  ).json()) as any;
  expect(item).toMatchObject({ stars: 135, observedAt: now });
  expect(item.trends[720]).toMatchObject({ delta: 35, gaps: [[start, now]] });
  expect(item.trends[720]).not.toHaveProperty("points");
  expect(item.trends[720].gaps).toEqual(history.gaps);
  external.mockClear();
  names[0] = "acme/renamed-without-listing";
  await run(start + 25 * 3_600_000);
  const daily = external.mock.calls.filter(([input]) =>
    String(input).includes("api.github.com"),
  );
  expect(JSON.parse(String(daily[0][1]?.body)).query).toContain(
    "repositoryTopics",
  );
  expect((await list()).items.find((item: any) => item.id === "R_0").name).toBe(
    names[0],
  );
});

test("keeps the ten-minute edge snapshot out of browser caches on misses and hits", async () => {
  await run();
  const stored = new Map<string, Response>();
  vi.stubGlobal("caches", {
    default: {
      match: async (request: Request) => stored.get(request.url)?.clone(),
      put: async (request: Request, response: Response) => {
        stored.set(request.url, response.clone());
      },
    },
  });
  const first = await api("/api/projects");
  expect(first.headers.get("Cache-Control")).toBe("no-store");
  const original = (await first.json()) as any;
  expect([...stored.values()][0].headers.get("Cache-Control")).toBe(
    "public, max-age=600",
  );
  stars[0] = 450;
  await run(now + 3_600_000);
  const hit = await api("/api/projects");
  expect(hit.headers.get("Cache-Control")).toBe("no-store");
  expect(await hit.json()).toEqual(original);
  stored.clear(); // The next read after the edge snapshot expires uses current observations.
  const fresh = await api("/api/projects");
  expect(fresh.headers.get("Cache-Control")).toBe("no-store");
  expect(((await fresh.json()) as any).items[0].stars).toBe(450);
  expect(
    (await api("/api/projects/R_0/history?window=24")).headers.get(
      "Cache-Control",
    ),
  ).toBe("no-store");
});

test("aggregates known partial deltas and negative values across all tracked projects", async () => {
  const start = now;
  await run(start);
  expect((await list()).summary).toMatchObject({
    net24: null,
    comparable24: 0,
  });
  stars = [400, 150];
  await run(start + 3_600_000);
  expect((await list()).items[0].trends[24]).toMatchObject({
    delta: 300,
    state: "partial",
  });
  expect(
    (await list("?q=useful&language=TypeScript&topic=tools")).summary,
  ).toMatchObject({ net24: 250, comparable24: 2 });
  stars[0] = 399;
  names.push("acme/gamma");
  stars.push(900);
  await run(start + 3 * HOUR);
  expect((await list()).summary).toMatchObject({
    net24: 249,
    comparable24: 2,
    accumulating: 1,
  });
});

test("combines filters, stable sorting, nulls-last and 100-item pagination; rejects invalid and write requests", async () => {
  names = Array.from(
    { length: 103 },
    (_, i) => `acme/project-${String(i).padStart(2, "0")}`,
  );
  stars = names.map(() => 100);
  const start = now;
  for (let i = 0; i < 6; i++) await run(start + i * 60_000);
  expect((await list()).total).toBe(103);
  expect((await list()).items).toHaveLength(100);
  expect((await list("?page=2")).items).toHaveLength(3);
  const combined = await list(
    "?q=USEFUL%20acme%2Fproject&language=Rust&topic=tools&sort=name&direction=asc",
  );
  expect(combined.total).toBe(102);
  expect(combined.items[0].name).toBe("acme/project-01");
  expect((await list("?topic=missing")).total).toBe(0);
  stars[0] = 90;
  await run(start + 3_600_000);
  // Newly discovered projects have no delta and must remain last in either direction.
  names.push("acme/zz-new");
  stars.push(100);
  await run(start + 3 * HOUR);
  expect((await list("?sort=24&direction=asc")).items[0].name).toBe(
    "acme/project-00",
  );
  expect((await list("?sort=24&direction=desc&page=2")).items.at(-1).name).toBe(
    "acme/zz-new",
  );
  expect((await list("?page=2")).summary).toMatchObject({
    net24: -10,
    comparable24: 103,
  });
  expect((await api("/api/projects?sort=invalid")).status).toBe(400);
  expect((await api("/api/projects?page=0")).status).toBe(400);
  expect((await api("/api/projects?view=all")).status).toBe(400);
  expect((await api("/api/projects/R_0/history?window=25")).status).toBe(400);
  expect(
    (
      await worker.fetch(
        new Request("https://worker.test/api/projects", { method: "POST" }),
        env,
        {} as ExecutionContext,
      )
    ).status,
  ).toBe(405);
  expect(
    (
      await worker.fetch(
        new Request("https://worker.test/api/projects", {
          headers: { Origin: "https://other.test" },
        }),
        env,
        {} as ExecutionContext,
      )
    ).status,
  ).toBe(403);
});

test("keeps protected overflow, removes lowest reliable growth at 04:00 Beijing, and restarts evicted tracking cycles", async () => {
  now = Date.parse("2026-10-01T17:00:00Z"); // Beijing 01:00
  const start = now;
  await setState(env.DB, "sampling_policy", { version: 2, effectiveAt: start }).run();
  names = Array.from({ length: 504 }, (_, i) => `acme/repo-${i}`);
  stars = names.map(() => 100);
  await seedProjects(start);
  rssNames = [];
  expect((await list()).total).toBe(504);
  stars[0] = 600;
  stars[1] = 200;
  // All projects get a second observation, but still lack 24h of history.
  for (let i = 0; i < 6; i++) await run(start + 23 * 3_600_000 + i * 1000);
  expect((await list()).total).toBe(504);
  for (let i = 0; i < 6; i++) await run(start + DAY + i * 60_000);
  const oldCycle = (await env.DB.prepare("SELECT * FROM projects WHERE id='R_10'").first<ProjectRow>())!;
  // A failure flag alone does not disqualify otherwise fresh, comparable observations.
  await env.DB.prepare("UPDATE projects SET error='GitHub repository unavailable' WHERE id='R_10'").run();
  await run(start + 27 * HOUR); // 04:00: normal 01:00 snapshots are still fresh.
  const cleaned = await list();
  expect(cleaned.total).toBe(500);
  expect(cleaned.status.cleanup).toMatchObject({ removed: 4, remaining: 500 });
  expect(cleaned.summary.fresh).toBe(500);
  expect((await (await api("/api/projects/R_0/history")).json()) as any).toMatchObject({ stale: false });
  expect((await list("?page=99")).page).toBe(5);
  expect((await api("/api/projects/R_10/history")).status).toBe(404);
  expect((await api("/api/projects/R_0/history")).status).toBe(200);
  rssNames = null;
  await run(start + 28 * HOUR); // 05:00
  const restored = (await (
    await api("/api/projects/R_10/history")
  ).json()) as any;
  expect(restored.points).toHaveLength(1);
  expect(restored.trend.state).toBe("accumulating");
  now += 60_000;
  vi.spyOn(Date, "now").mockReturnValue(now);
  // The late result is newer than re-entry, but belongs to the deleted tracking cycle.
  await saveObservations(env.DB, [{ repo: { id: "R_10", stargazerCount: 999 }, previous: oldCycle, listedAt: null }], now);
  expect(await (await api("/api/projects/R_10/history")).json()).toEqual(restored);
  expect((await list("?q=acme%2Frepo-10")).items.find((p: any) => p.id === "R_10"))
    .toMatchObject({ stars: 100, observedAt: restored.observedAt });
  expect((await list()).total).toBe(504);
  fail = true;
  rssNames = [];
  await run(start + 48 * 3_600_000);
  await run(start + 51 * HOUR); // next 04:00, failed observations cannot be scored.
  const deferred = await list();
  expect(deferred.total).toBe(504);
  expect(deferred.status.cleanup).toMatchObject({ removed: 0, eligible: 0 });
  expect(deferred.items.every((p: any) => p.stale)).toBe(true);
  expect((await (await api("/api/projects/R_0/history")).json()) as any).toMatchObject({ stale: true });
}, 60_000);

test("rotates failed discoveries and preserves their 30-minute cooldown across rediscovery", async () => {
  now = Date.parse("2026-10-01T00:50:00Z"); // Beijing 08:50, shortly before source refresh.
  names = Array.from({ length: 21 }, (_, i) => `acme/new-${i}`);
  stars = names.map(() => 100);
  failedIds = new Set(names.slice(0, 20).map((_, i) => `R_${i}`));
  const firstSeen = now;
  const lookups = () => external.mock.calls.filter(([, init]) =>
    init?.body && !JSON.parse(String(init.body)).query.includes("nodes("));
  await run();
  expect((await list()).total).toBe(0);
  expect((await list()).status.collection.error).toBe("GitHub repository unavailable");
  rssNames = [];
  await run(firstSeen + 5 * 60_000);
  expect((await list()).items[0]).toMatchObject({
    name: "acme/new-20", listedAt: firstSeen, enteredAt: firstSeen + 5 * 60_000,
  });
  failedIds.clear();
  rssNames = null;
  await run(firstSeen + 10 * 60_000); // Source rediscovers the failed projects at 09:00.
  await run(firstSeen + 29 * 60_000);
  expect(lookups()).toHaveLength(2);
  expect((await list()).total).toBe(1);
  await run(firstSeen + 30 * 60_000);
  expect((await list()).total).toBe(21);
});

test("the worked retention scores favor recent acceleration over historical popularity", async () => {
  const day = 24 * 3_600_000;
  const start = Date.parse("2026-09-01T20:00:00Z");
  names = ["acme/accelerating", "acme/steady", "acme/cooling", "acme/stalled"];
  stars = [18000, 17000, 11000, 11000];
  await run(start);
  stars = [18600, 19300, 19790, 20000];
  await run(start + 23 * day);
  stars = [19100, 19700, 19940, 20000];
  await run(start + 27 * day);
  stars = [19500, 19900, 19990, 20000];
  await run(start + 29 * day);
  // 499 new protected projects leave room for precisely one mature project.
  names.push(...Array.from({ length: 499 }, (_, i) => `acme/protected-${i}`));
  stars.push(...Array(499).fill(100));
  await seedProjects(start + 29 * day + HOUR, 4);
  rssNames = [];
  stars.fill(20000, 0, 4);
  for (let i = 0; i < 6; i++) await run(start + 30 * day - 600_000 + i * 1000);
  await run(start + 30 * day);
  expect((await list()).total).toBe(500);
  expect((await api("/api/projects/R_0/history?window=720")).status).toBe(200);
  for (const id of ["R_1", "R_2", "R_3"])
    expect((await api(`/api/projects/${id}/history`)).status).toBe(404);
}, 60_000);

test("persists independent source and detail progress, keeps original listing times and deduplicates across sources", async () => {
  const page = "https://hellogithub.com/repository/abc123";
  const hello = SOURCES.find((source) => source.id === "hellogithub-all")!.url;
  const weekly = SOURCES.find((source) => source.id === "github-weekly")!.url;
  const markdown = SOURCES.find(
    (source) => source.id === "opengithubs-daily",
  )!.url;
  sourceResponses.set(
    hello,
    () =>
      new Response(
        `<rss><channel><item><link>${page}</link></item></channel></rss>`,
      ),
  );
  sourceResponses.set(
    weekly,
    () => new Response("private response must not escape", { status: 503 }),
  );
  sourceResponses.set(
    markdown,
    () =>
      new Response(
        "https://github.com/ACME/alpha/tree/main https://github.com/OpenGithubs/weekly",
      ),
  );
  sourceResponses.set(page, () => new Response("not embedded project data"));
  const firstSeen = now;
  await run();
  expect((await list()).total).toBe(2);
  expect((await list()).status.discovery.failed).toBe(2);
  const calls = (url: string) =>
    external.mock.calls.filter(([input]) => String(input) === url).length;
  const sourceCalls = calls(hello);
  sourceResponses.set(weekly, () => new Response("<rss><channel/></rss>"));
  sourceResponses.set(
    page,
    () =>
      new Response(
        `<script id="__NEXT_DATA__">${JSON.stringify({ props: { pageProps: { repo: { url: "https://github.com/acme/new" } } } })}</script>`,
      ),
  );
  names.push("acme/new");
  stars.push(300);
  await run(firstSeen + 10 * 60_000);
  expect((await list()).status.discovery.failed).toBe(2);
  expect(calls(page)).toBe(1);
  await run(firstSeen + 30 * 60_000);
  const result = await list();
  expect(result.total).toBe(3);
  expect(result.status.discovery.failed).toBe(0);
  expect(
    result.items.find((item: any) => item.name === "acme/new"),
  ).toMatchObject({
    listedAt: firstSeen,
    enteredAt: firstSeen + 30 * 60_000,
  });
  expect(calls(hello)).toBe(sourceCalls);
  expect(JSON.stringify(result)).not.toContain("private response");
});
