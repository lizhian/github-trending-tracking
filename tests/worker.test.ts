import { afterAll, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { Miniflare, convertV4MiniflareOptions } from "miniflare";
import { readFileSync } from "node:fs";
import worker, { type Env } from "../worker/index";

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
const external = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
  if (String(input).includes("rsshub") && rssFailure)
    return new Response("test-secret", { status: 503 });
  if (String(input).includes("rsshub"))
    return new Response(
      `<rss><channel>${(rssNames ?? names).map((name) => `<item><link>https://github.com/${name}</link></item>`).join("")}</channel></rss>`,
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
  await env.DB.exec(
    readFileSync("migrations/0001_tracking.sql", "utf8")
      .replace(/--[^\n]*/g, "")
      .replace(/\n/g, " "),
  );
});
beforeEach(async () => {
  vi.restoreAllMocks();
  vi.stubGlobal("fetch", external);
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
});
afterAll(async () => {
  vi.unstubAllGlobals();
  await mf.dispose();
});

test("discovers, deduplicates by GitHub identity, samples across hours and recovers from upstream failure", async () => {
  await run();
  expect((await list()).total).toBe(2);
  expect((await list()).items[0].trends["24"].state).toBe("accumulating");
  await run(now + 10 * 60_000);
  names[0] = "acme/renamed";
  stars[0] = 125;
  await run(now + 3_600_000);
  let result = await list();
  expect(result.total).toBe(2);
  expect(result.items[0]).toMatchObject({ name: "acme/renamed", stars: 125 });
  expect(result.items[0].trends["24"]).toMatchObject({
    delta: 25,
    state: "partial",
  });
  fail = true;
  await run(now + 3_600_000);
  expect((await list()).items[0]).toMatchObject({
    stars: 125,
    error: "GitHub HTTP 503",
  });
  fail = false;
  stars[0] = 120;
  await run(now + 3_600_000);
  const history = (await (
    await api("/api/projects/R_0/history?window=24")
  ).json()) as any;
  expect(history.points.map((p: number[]) => p[1])).toEqual([100, 125, 120]);
  expect(history.trend.delta).toBe(20);
  expect(history.gaps).toHaveLength(1);
  failedIds.add("R_1");
  rssFailure = true;
  stars[0] = 140;
  await run(now + 3_600_000);
  result = await list();
  expect(result.items.find((p: any) => p.id === "R_0").stars).toBe(140);
  expect(result.items.find((p: any) => p.id === "R_1")).toMatchObject({
    stars: 200,
    error: "GitHub repository unavailable",
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
  await run(start + 31 * 24 * 3_600_000);
  result = await list();
  expect(result.items[0].trends["720"]).toMatchObject({
    delta: -10,
    state: "complete",
  });
  const retained = (await (
    await api("/api/projects/R_0/history?window=720")
  ).json()) as any;
  expect(retained.points).toHaveLength(2);
  now = start + 32 * 24 * 3_600_000;
  vi.spyOn(Date, "now").mockReturnValue(now);
  expect((await list()).items[0].trends["720"]).toMatchObject({
    delta: null,
    state: "accumulating",
  });
});

test("combines filters, stable sorting, nulls-last and 50-item pagination; rejects invalid and write requests", async () => {
  names = Array.from(
    { length: 53 },
    (_, i) => `acme/project-${String(i).padStart(2, "0")}`,
  );
  stars = names.map(() => 100);
  const start = now;
  for (let i = 0; i < 3; i++) await run(start + i * 60_000);
  expect((await list()).total).toBe(53);
  expect((await list()).items).toHaveLength(50);
  expect((await list("?page=2")).items).toHaveLength(3);
  const combined = await list(
    "?q=USEFUL%20acme%2Fproject&language=Rust&topic=tools&sort=name&direction=asc",
  );
  expect(combined.total).toBe(52);
  expect(combined.items[0].name).toBe("acme/project-01");
  expect((await list("?topic=missing")).total).toBe(0);
  stars[0] = 90;
  await run(start + 3_600_000);
  // Newly discovered projects have no delta and must remain last in either direction.
  names.push("acme/zz-new");
  stars.push(100);
  await run(start + 3_660_000);
  expect((await list("?sort=24&direction=asc")).items[0].name).toBe(
    "acme/project-00",
  );
  expect((await list("?sort=24&direction=desc&page=2")).items.at(-1).name).toBe(
    "acme/zz-new",
  );
  expect((await api("/api/projects?sort=invalid")).status).toBe(400);
  expect((await api("/api/projects?page=0")).status).toBe(400);
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
  now = Date.parse("2026-10-01T20:00:00Z");
  const start = now;
  names = Array.from({ length: 1004 }, (_, i) => `acme/repo-${i}`);
  stars = names.map(() => 100);
  for (let i = 0; i < 51; i++) {
    await run(start + i * 1000);
    rssNames = [];
  }
  expect((await list()).total).toBe(1004);
  stars[0] = 600;
  stars[1] = 200;
  // All projects get a second observation, but still lack 24h of history.
  for (let i = 0; i < 6; i++) await run(start + 23 * 3_600_000 + i * 1000);
  expect((await list()).total).toBe(1004);
  await run(start + 24 * 3_600_000);
  const cleaned = await list();
  expect(cleaned.total).toBe(1000);
  expect(cleaned.status.cleanup).toMatchObject({ removed: 4, remaining: 1000 });
  expect((await api("/api/projects/R_10/history")).status).toBe(404);
  expect((await api("/api/projects/R_0/history")).status).toBe(200);
  rssNames = null;
  await run(start + 24 * 3_600_000 + 600_000);
  const restored = (await (
    await api("/api/projects/R_10/history")
  ).json()) as any;
  expect(restored.points).toHaveLength(1);
  expect(restored.trend.state).toBe("accumulating");
  expect((await list()).total).toBe(1004);
  fail = true;
  rssNames = [];
  await run(start + 48 * 3_600_000);
  const deferred = await list();
  expect(deferred.total).toBe(1004);
  expect(deferred.status.cleanup).toMatchObject({ removed: 0, eligible: 0 });
  expect(deferred.items.every((p: any) => p.stale)).toBe(true);
}, 60_000);

test("rotates failed discoveries so other new projects can still enter tracking", async () => {
  names = Array.from({ length: 21 }, (_, i) => `acme/new-${i}`);
  stars = names.map(() => 100);
  failedIds = new Set(names.slice(0, 20).map((_, i) => `R_${i}`));
  await run();
  expect((await list()).total).toBe(0);
  await run(now + 600_000);
  expect((await list()).items[0].name).toBe("acme/new-20");
  failedIds.clear();
  await run(now + 600_000);
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
  // 999 new protected projects leave room for precisely one mature project.
  names.push(...Array.from({ length: 999 }, (_, i) => `acme/protected-${i}`));
  stars.push(...Array(999).fill(100));
  for (let i = 0; i < 50; i++) {
    await run(start + 29 * day + 3_600_000 + i * 1000);
    rssNames = [];
  }
  stars.fill(20000, 0, 4);
  for (let i = 0; i < 6; i++) await run(start + 30 * day - 600_000 + i * 1000);
  await run(start + 30 * day);
  expect((await list()).total).toBe(1000);
  expect((await api("/api/projects/R_0/history?window=720")).status).toBe(200);
  for (const id of ["R_1", "R_2", "R_3"])
    expect((await api(`/api/projects/${id}/history`)).status).toBe(404);
}, 60_000);
