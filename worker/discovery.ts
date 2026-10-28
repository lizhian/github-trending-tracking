import { XMLParser, XMLValidator } from "fast-xml-parser";
import { type RunState } from "./contracts";
import { DAY, HOUR } from "./trends";
import { DeferredRequest, safeError, type Upstream } from "./upstream";
import { latestSlot, nextSlot as nextSamplingSlot, type SamplingPolicy } from "./schedule";

const RSS = "https://rsshub-balancer.virworks.moe";
export const SOURCES = [
  {
    id: "github-daily", kind: "github",
    url: `${RSS}/github/trending/daily/any`, hourly: true,
  },
  {
    id: "github-weekly", kind: "github",
    url: `${RSS}/github/trending/weekly/any`, hourly: false,
  },
  {
    id: "github-monthly", kind: "github",
    url: `${RSS}/github/trending/monthly/any`, hourly: false,
  },
  {
    id: "hellogithub-all", kind: "hellogithub",
    url: `${RSS}/hellogithub/home/all`, hourly: false,
  },
  {
    id: "hellogithub-featured", kind: "hellogithub",
    url: `${RSS}/hellogithub/home/featured`, hourly: false,
  },
  ...["daily", "weekly", "monthly"].map((period) => ({
    id: `opengithubs-${period}`, kind: "markdown",
    url: `https://raw.githubusercontent.com/OpenGithubs/github-${period}-rank/refs/heads/main/README.md`,
    hourly: false,
  })),
] as const;

interface SourceState extends RunState {
  nextAt: number;
}
export interface Discovery {
  name: string;
  listedAt: number;
}
interface DetailTask {
  url: string;
  listedAt: number;
  nextAt: number;
  error: string | null;
}
export interface DiscoveryState {
  scheduleVersion?: number;
  sources: Record<string, SourceState>;
  details: DetailTask[];
  resolved: Record<string, { day: number; name: string }>;
}
export function emptyDiscovery(): DiscoveryState {
  return { sources: {}, details: [], resolved: {} };
}
const beijingDay = (at: number) => Math.floor((at + 8 * HOUR) / DAY);
function nextSlot(at: number, hourly: boolean, policy?: SamplingPolicy): number {
  // 09:00 Beijing is 01:00 UTC, independent of the runtime timezone.
  return hourly
    ? policy && at >= policy.effectiveAt ? nextSamplingSlot(at) : (Math.floor(at / HOUR) + 1) * HOUR
    : (Math.floor((at - HOUR) / DAY) + 1) * DAY + HOUR;
}

export function repositoryName(candidate: string, allowSubpath = false): string | null {
  const match = candidate.trim().match(allowSubpath
    ? /^https?:\/\/github\.com\/([\w.-]+)\/([\w.-]+)(?:[/?#].*)?$/i
    : /^https:\/\/github\.com\/([\w.-]+)\/([\w.-]+)\/?(?:[?#].*)?$/i);
  if (!match || [
    "trending", "topics", "collections", "sponsors", "features", "marketplace",
    "settings", "orgs", "users", "search",
  ].includes(match[1].toLowerCase())) return null;
  const repo = match[2].replace(/\.git$/i, "");
  if (!repo || repo === "." || repo === ".." || match[1] === "." || match[1] === "..") return null;
  return `${match[1]}/${repo}`;
}
function rssLinks(xml: string): string[][] {
  if (XMLValidator.validate(xml) !== true) throw new Error("RSS invalid XML");
  const parsed = new XMLParser({ ignoreAttributes: false, processEntities: false }).parse(xml);
  if (!parsed.rss || !Object.hasOwn(parsed.rss, "channel"))
    throw new Error("RSS missing channel");
  const items = parsed.rss.channel.item ?? [];
  return (Array.isArray(items) ? items : [items]).map((item) => [
    item.link, typeof item.guid === "string" ? item.guid : item.guid?.["#text"],
  ].filter((candidate): candidate is string => typeof candidate === "string"));
}
export function parseGithubRSS(xml: string): string[] {
  return rssLinks(xml).flatMap((links) => {
    const name = links.map((link) => repositoryName(link)).find(Boolean);
    return name ? [name] : [];
  });
}
export function parseHelloRSS(xml: string): string[] {
  return rssLinks(xml).flatMap((links) => {
    const link = links.map((candidate) => candidate.trim()).find((candidate) =>
      /^https:\/\/hellogithub\.com\/repository\/[a-f\d]+\/?(?:[?#].*)?$/i.test(candidate));
    return link ? [link.split(/[?#]/)[0].replace(/\/$/, "")] : [];
  });
}
export function parseHelloProject(html: string): string {
  const script = html.match(/<script\b[^>]*\bid=["']__NEXT_DATA__["'][^>]*>([\s\S]*?)<\/script\s*>/i);
  try {
    const url: unknown = script && JSON.parse(script[1]).props?.pageProps?.repo?.url;
    const name = typeof url === "string" ? repositoryName(url) : null;
    if (name) return name;
  } catch { /* Malformed embedded data is a failed detail, never a prose fallback. */ }
  throw new Error("HelloGithub invalid project");
}
export function parseMarkdown(text: string): string[] {
  const names = new Map<string, string>();
  for (const match of text.matchAll(/\bhttps?:\/\/github\.com\/([\w.-]+)\/([\w.-]+)(?:\/[^\s<>"'\[\]()，。；：！？]*)?/gi)) {
    // Punctuation ending a prose URL is not part of the repository name.
    const name = repositoryName(match[0].replace(/[.,;:!?]+$/, ""), true);
    if (name && name.split("/")[0].toLowerCase() !== "opengithubs")
      names.set(name.toLowerCase(), name);
  }
  return [...names.values()];
}

export async function discover(
  upstream: Upstream,
  state: DiscoveryState,
  now: number,
  prior: RunState | null,
  policy?: SamplingPolicy,
): Promise<{ entries: Discovery[]; status: RunState; state: DiscoveryState; changed: boolean }> {
  const deadline = upstream.clock.now() + 3 * 60_000;
  const day = beijingDay(now);
  let changed = false;
  if (policy && now >= policy.effectiveAt && state.scheduleVersion !== policy.version) {
    const daily = state.sources["github-daily"];
    if (daily) {
      const next = daily.succeededAt === null ? latestSlot(now) : nextSamplingSlot(daily.succeededAt);
      daily.nextAt = Math.max(next, daily.failed ? daily.nextAt : 0, upstream.blockedUntil(SOURCES[0].url));
    }
    state.scheduleVersion = policy.version;
    changed = true;
  }
  state.resolved = Object.fromEntries(
    Object.entries(state.resolved).filter(([, value]) => value.day === day),
  );
  const entries = new Map<string, Discovery>();
  const add = (name: string, listedAt: number) => {
    const previous = entries.get(name.toLowerCase());
    if (!previous || previous.listedAt < listedAt)
      entries.set(name.toLowerCase(), { name, listedAt });
  };
  let attempted = false;
  let succeeded = false;
  for (const source of SOURCES) {
    const previous = state.sources[source.id];
    if ((previous?.nextAt ?? 0) > now) continue;
    try {
      const text = await upstream.text(
        source.url, source.kind === "markdown" ? "OpenGithubs" : "RSS", {}, deadline,
      );
      attempted = true;
      const values = source.kind === "markdown" ? parseMarkdown(text)
        : source.kind === "github" ? parseGithubRSS(text) : parseHelloRSS(text);
      if (source.kind === "hellogithub") {
        for (const url of new Set(values)) {
          const cached = state.resolved[url];
          if (cached) add(cached.name, now);
          else if (!state.details.some((task) => task.url === url))
            state.details.push({ url, listedAt: now, nextAt: now, error: null });
        }
      } else for (const name of values) add(name, now);
      state.sources[source.id] = {
        attemptedAt: now, succeededAt: now, succeeded: values.length, failed: 0, error: null,
        nextAt: nextSlot(upstream.clock.now(), source.hourly, policy),
      };
      succeeded = true;
    } catch (error) {
      if (error instanceof DeferredRequest) {
        if (error.retryAt === null) break;
        continue;
      }
      attempted = true;
      state.sources[source.id] = {
        attemptedAt: now, succeededAt: previous?.succeededAt ?? null,
        succeeded: 0, failed: 1, error: safeError(error),
        nextAt: Math.max(upstream.clock.now() + 30 * 60_000, upstream.blockedUntil(source.url)),
      };
    }
  }
  let detailRequests = 0;
  // Older failed details wait for their retry time; new details can still proceed.
  for (const task of [...state.details]) {
    if (detailRequests >= 20) break;
    if (task.nextAt > now) continue;
    detailRequests++;
    try {
      const text = await upstream.text(task.url, "HelloGithub", {}, deadline);
      attempted = true;
      const name = parseHelloProject(text);
      add(name, task.listedAt);
      state.resolved[task.url] = { day: beijingDay(upstream.clock.now()), name };
      state.details.splice(state.details.indexOf(task), 1);
      succeeded = true;
    } catch (error) {
      if (error instanceof DeferredRequest) {
        detailRequests--;
        if (error.retryAt === null) break;
        continue;
      }
      attempted = true;
      task.error = safeError(error);
      task.nextAt = Math.max(upstream.clock.now() + 30 * 60_000, upstream.blockedUntil(task.url));
    }
  }
  const errors = [
    ...Object.entries(state.sources).flatMap(([id, value]) => value.failed ? [`${id}: ${value.error}`] : []),
    ...state.details.flatMap((task) => task.error ? [`hellogithub-detail: ${task.error}`] : []),
  ];
  return {
    entries: [...entries.values()], state, changed: changed || attempted,
    status: {
      attemptedAt: attempted ? now : prior?.attemptedAt ?? now,
      succeededAt: succeeded ? now : prior?.succeededAt ?? null,
      succeeded: attempted ? entries.size : prior?.succeeded ?? 0,
      failed: errors.length, error: errors[0] ?? null,
    },
  };
}
