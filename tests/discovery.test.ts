import { afterEach, expect, test, vi } from "vitest";
import {
  discover, emptyDiscovery, parseGithubRSS, parseHelloProject, parseHelloRSS,
  parseMarkdown, SOURCES,
} from "../worker/discovery";
import { DeferredRequest, Upstream, type Clock } from "../worker/upstream";
import { repositories } from "../worker/github";
import { DAY, HOUR } from "../worker/trends";
import { type SamplingPolicy } from "../worker/schedule";

function harness(at = Date.parse("2026-10-01T01:00:00Z")) {
  let time = at;
  const calls: { url: string; at: number }[] = [];
  const responses = new Map<string, () => Response | Promise<Response>>();
  const clock: Clock = {
    now: () => time,
    sleep: async (ms) => { time += ms; },
  };
  const hosts = {};
  const upstream = new Upstream(hosts, clock);
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    calls.push({ url, at: time });
    return responses.get(url)?.() ?? new Response(url.includes("raw.githubusercontent.com") ? "" : "<rss><channel/></rss>");
  }));
  return {
    upstream, responses, calls,
    setTime: (at: number) => { time = at; },
    count: (url: string) => calls.filter((call) => call.url === url).length,
    source: (id: string) => SOURCES.find((source) => source.id === id)!.url,
  };
}
const policy: SamplingPolicy = { version: 2, effectiveAt: Date.parse("2026-09-01T00:00:00Z") };
const rss = (...links: string[]) => `<rss><channel>${links.map((link) => `<item><link>${link}</link></item>`).join("")}</channel></rss>`;
const detail = (url: string) => `<a href="https://github.com/irrelevant/recommendation">推荐</a><script type="application/json" id="__NEXT_DATA__">${JSON.stringify({ props: { pageProps: { repo: { url } } } })}</script>`;
afterEach(() => { vi.unstubAllGlobals(); });

test("daily trending follows the nine slots, normalizes old nextAt, retries failures and skips missed rounds", async () => {
  const at = (time: string) => Date.parse(`${time}+08:00`);
  const h = harness(at("2026-10-01T08:40:00"));
  const policy: SamplingPolicy = { version: 2, effectiveAt: h.upstream.clock.now() };
  let result = await discover(h.upstream, emptyDiscovery(), h.upstream.clock.now(), null, policy);
  expect(h.calls).toHaveLength(8);
  result.state = JSON.parse(JSON.stringify(result.state));
  const rebooted = new Upstream(JSON.parse(JSON.stringify(h.upstream.hosts)), h.upstream.clock);
  const step = async (time: string) => {
    h.setTime(at(time));
    result = await discover(rebooted, result.state, h.upstream.clock.now(), result.status, policy);
  };
  await step("2026-10-01T08:50:00");
  expect(h.calls).toHaveLength(8);
  await step("2026-10-01T09:00:00");
  expect(h.calls).toHaveLength(16);
  // A persisted hourly timer must not cause an extra successful read at 10:00.
  result.state.scheduleVersion = undefined;
  result.state.sources["github-daily"].nextAt = at("2026-10-01T10:00:00");
  await step("2026-10-01T10:00:00");
  expect(h.calls).toHaveLength(16);
  h.responses.set(h.source("github-daily"), () => new Response("unavailable", { status: 503 }));
  await step("2026-10-01T11:00:00");
  expect(h.calls).toHaveLength(17);
  result.state.scheduleVersion = undefined; // normalization must retain pending backoff.
  await step("2026-10-01T11:20:00");
  expect(h.calls).toHaveLength(17);
  h.responses.delete(h.source("github-daily"));
  await step("2026-10-01T11:30:00");
  expect(h.calls).toHaveLength(18);
  await step("2026-10-01T21:00:00");
  expect(h.calls).toHaveLength(19);
  await step("2026-10-01T23:00:00");
  expect(h.calls).toHaveLength(19);
  await step("2026-10-02T01:00:00");
  expect(h.calls).toHaveLength(20);
  expect(h.count(h.source("github-weekly"))).toBe(2); // initial read + daily 09:00
  await step("2026-10-04T10:20:00"); // Catch up each source once after several missed days.
  expect(h.calls).toHaveLength(28);
  expect(h.count(h.source("github-weekly"))).toBe(3);
  await step("2026-10-04T10:20:00");
  expect(h.calls).toHaveLength(28);
});

test("extracts authoritative RSS and HelloGithub projects and all Markdown repository links", () => {
  expect(parseGithubRSS(`<rss><channel>
    <item><link>https://github.com/acme/alpha.git</link><description>https://github.com/wrong/prose</description></item>
    <item><link>https://example.com/other</link><guid isPermaLink="true">https://github.com/acme/beta</guid></item>
    <item><link>https://github.com/topics/tools</link></item>
    <item><link>https://github.com/acme/alpha/issues</link></item>
  </channel></rss>`)).toEqual(["acme/alpha", "acme/beta"]);
  expect(parseGithubRSS(rss())).toEqual([]);
  expect(() => parseGithubRSS("<rss>")).toThrow("RSS invalid XML");
  expect(() => parseGithubRSS("<rss/>")).toThrow("RSS missing channel");
  expect(parseHelloRSS(rss("https://hellogithub.com/repository/abc123/?q=1", "https://example.com/repository/abc123")))
    .toEqual(["https://hellogithub.com/repository/abc123"]);
  expect(parseHelloProject(detail("https://github.com/acme/main"))).toBe("acme/main");
  expect(() => parseHelloProject('<a href="https://github.com/wrong/link">源码</a>')).toThrow("HelloGithub invalid project");
  expect(() => parseHelloProject('<script id="__NEXT_DATA__">broken</script>')).toThrow("HelloGithub invalid project");
  expect(parseMarkdown(`https://github.com/acme/one。
    [two](https://github.com/acme/two.git) <a href="https://github.com/acme/three/tree/main">three</a>
    https://github.com/ACME/one, https://github.com/OpenGithubs/weekly
    https://github.com/opengithubs/arbitrary https://github.com/OpenGithubs
    https://github.com/topics/tools https://github.com.evil.com/wrong/repo
    https://github.com/acme/four/issues/2#comment
  `).map((name) => name.toLowerCase()).sort()).toEqual(["acme/four", "acme/one", "acme/three", "acme/two"]);
});

test("retains unresolved source failures across other source successes and retries after 30 minutes", async () => {
  const h = harness();
  const url = h.source("opengithubs-weekly");
  h.responses.set(url, () => new Response("private details", { status: 503 }));
  let result = await discover(h.upstream, emptyDiscovery(), h.upstream.clock.now(), null, policy);
  expect(result.status).toMatchObject({ failed: 1, error: "opengithubs-weekly: OpenGithubs HTTP 503" });
  h.setTime(Date.parse("2026-10-01T01:20:00Z"));
  result = await discover(h.upstream, result.state, h.upstream.clock.now(), result.status, policy);
  expect(h.count(url)).toBe(1);
  expect(result.status.failed).toBe(1);
  h.setTime(Date.parse("2026-10-01T03:00:00Z")); // 11:00: daily trending succeeds, weekly source still fails.
  result = await discover(h.upstream, result.state, h.upstream.clock.now(), result.status, policy);
  expect(result.status.failed).toBe(1);
  h.responses.set(url, () => new Response("https://github.com/acme/recovered"));
  h.setTime(Date.parse("2026-10-01T03:30:00Z"));
  result = await discover(h.upstream, result.state, h.upstream.clock.now(), result.status, policy);
  expect(h.count(url)).toBe(3);
  expect(result.status.failed).toBe(0);
  expect(result.entries).toContainEqual({ name: "acme/recovered", listedAt: h.upstream.clock.now() });
});

test("shares daily HelloGithub results, limits failed detail requests to 20 and keeps listing timestamps", async () => {
  const h = harness();
  const start = h.upstream.clock.now();
  const pages = Array.from({ length: 21 }, (_, i) => `https://hellogithub.com/repository/${i.toString(16)}`);
  h.responses.set(h.source("hellogithub-all"), () => new Response(rss(...pages)));
  h.responses.set(h.source("hellogithub-featured"), () => new Response(rss(...pages)));
  for (const [i, page] of pages.entries())
    h.responses.set(page, () => new Response("invalid page", { status: i % 2 ? 503 : 200 }));
  let result = await discover(h.upstream, emptyDiscovery(), start, null, policy);
  expect(h.calls.filter((call) => call.url.startsWith("https://hellogithub.com/"))).toHaveLength(20);
  expect(result.state.details).toHaveLength(21);
  expect(result.status.failed).toBe(20);
  h.setTime(start + 10 * 60_000);
  h.responses.set(pages[20], () => new Response(detail("https://github.com/acme/last")));
  result = await discover(h.upstream, JSON.parse(JSON.stringify(result.state)), h.upstream.clock.now(), result.status, policy);
  expect(result.entries).toEqual([{ name: "acme/last", listedAt: start }]);
  expect(h.count(pages[0])).toBe(1);
  expect(h.count(h.source("hellogithub-all"))).toBe(1);
  h.setTime(start + DAY);
  for (const page of pages) h.responses.set(page, () => new Response(detail("https://github.com/acme/updated")));
  result = await discover(h.upstream, result.state, h.upstream.clock.now(), result.status, policy);
  expect(h.count(pages[20])).toBe(1); // The batch is bounded even after the next day's RSS.
  h.setTime(start + DAY + 10 * 60_000);
  result = await discover(h.upstream, result.state, h.upstream.clock.now(), result.status, policy);
  expect(h.count(pages[20])).toBe(2);
  expect(result.status.failed).toBe(0);
  expect(result.entries[0].name).toBe("acme/updated");
});

test("reuses successful HelloGithub details within a Beijing day and refreshes corrected addresses the next day", async () => {
  const h = harness();
  const start = h.upstream.clock.now();
  const page = "https://hellogithub.com/repository/abc123";
  const featured = h.source("hellogithub-featured");
  h.responses.set(h.source("hellogithub-all"), () => new Response(rss(page)));
  h.responses.set(featured, () => new Response("unavailable", { status: 503 }));
  h.responses.set(page, () => new Response(detail("https://github.com/acme/original")));
  let result = await discover(h.upstream, emptyDiscovery(), start, null, policy);
  expect(h.count(page)).toBe(1);
  h.setTime(start + HOUR);
  h.responses.set(featured, () => new Response(rss(page)));
  result = await discover(h.upstream, result.state, h.upstream.clock.now(), result.status, policy);
  expect(h.count(page)).toBe(1);
  expect(result.entries).toEqual([{ name: "acme/original", listedAt: start + HOUR }]);
  h.setTime(start + DAY);
  h.responses.set(page, () => new Response(detail("https://github.com/acme/corrected")));
  result = await discover(h.upstream, result.state, h.upstream.clock.now(), result.status, policy);
  expect(h.count(page)).toBe(2);
  expect(result.entries).toEqual([{ name: "acme/corrected", listedAt: start + DAY }]);
});

test("paces same-host calls and persists whole-host rate-limit cooldown with both Retry-After formats", async () => {
  const h = harness();
  const first = h.source("github-daily");
  const second = h.source("github-weekly");
  await h.upstream.text(first, "RSS");
  await h.upstream.text(second, "RSS");
  expect(h.calls[1].at - h.calls[0].at).toBe(2000);
  h.responses.set(first, () => new Response("rate limited", { status: 429, headers: { "Retry-After": "7200" } }));
  await expect(h.upstream.text(first, "RSS")).rejects.toThrow("RSS HTTP 429");
  const blocked = h.upstream.blockedUntil(first);
  const rebooted = new Upstream(JSON.parse(JSON.stringify(h.upstream.hosts)), h.upstream.clock);
  await expect(rebooted.text(second, "RSS")).rejects.toBeInstanceOf(DeferredRequest);
  expect(h.count(second)).toBe(1);
  h.setTime(blocked);
  await rebooted.text(second, "RSS");
  const retryDate = new Date(blocked + 3 * HOUR).toUTCString();
  h.responses.set(first, () => new Response("rate limited", { status: 403, headers: { "Retry-After": retryDate } }));
  await expect(rebooted.text(first, "RSS")).rejects.toThrow("RSS HTTP 403");
  expect(rebooted.blockedUntil(first)).toBe(Date.parse(retryDate));
});

test("isolates limited hosts, handles GraphQL rate limits and preserves partial successful repositories", async () => {
  const h = harness();
  h.responses.set(h.source("github-daily"), () => new Response("private", { status: 429 }));
  const result = await discover(h.upstream, emptyDiscovery(), h.upstream.clock.now(), null, policy);
  expect(result.status.failed).toBe(1);
  expect(h.count(h.source("github-weekly"))).toBe(0);
  expect(h.count(h.source("opengithubs-daily"))).toBe(1);
  const url = "https://api.github.com/graphql";
  const repo = { id: "R_1", nameWithOwner: "acme/one", stargazerCount: 1, repositoryTopics: { nodes: [] } };
  h.responses.set(url, () => Response.json({ data: { r0: repo, r1: null }, errors: [{ type: "RATE_LIMITED" }] }));
  expect(await repositories("secret", ["acme/one", "acme/two"], false, h.upstream)).toEqual([repo, null]);
  await expect(repositories("secret", ["acme/two"], false, h.upstream)).rejects.toBeInstanceOf(DeferredRequest);
  expect(h.count(url)).toBe(1);
  h.setTime(h.upstream.blockedUntil(url));
  const resetAt = h.upstream.clock.now() + 3 * HOUR;
  h.responses.set(url, () => Response.json({ data: { r0: repo } }, {
    headers: {
      "Retry-After": "7200", "X-RateLimit-Remaining": "0",
      "X-RateLimit-Reset": String(resetAt / 1000),
    },
  }));
  expect(await repositories("secret", ["acme/one"], false, h.upstream)).toEqual([repo]);
  expect(h.upstream.blockedUntil(url)).toBe(resetAt);
});

test("stops discovery at its time budget and leaves remaining details for later", async () => {
  const h = harness();
  const start = h.upstream.clock.now();
  const pages = Array.from({ length: 20 }, (_, i) => `https://hellogithub.com/repository/${i.toString(16)}`);
  h.responses.set(h.source("hellogithub-all"), () => new Response(rss(...pages)));
  for (const page of pages) h.responses.set(page, async () => {
    await h.upstream.clock.sleep(15_000);
    return new Response(detail("https://github.com/acme/one"));
  });
  const result = await discover(h.upstream, emptyDiscovery(), start, null, policy);
  expect(h.upstream.clock.now() - start).toBeLessThanOrEqual(3 * 60_000);
  expect(result.state.details.length).toBeGreaterThan(0);
  expect(result.state.details.length).toBeLessThan(20);
  expect(result.entries).toHaveLength(1);
});
