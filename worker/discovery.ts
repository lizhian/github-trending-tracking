import { XMLParser, XMLValidator } from "fast-xml-parser";
import { PROJECT_TYPES, type ProjectType, type RunState } from "./contracts";
import { DAY, HOUR } from "./trends";
import { DeferredRequest, safeError, type Upstream } from "./upstream";
import { latestSlot, nextSlot as nextSamplingSlot, type SamplingPolicy } from "./schedule";

const RSS = "https://rsshub-balancer.virworks.moe";
const DISCOVERY_BUDGET = 3 * 60_000;
const RETRY_DELAY = 30 * 60_000;
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
  {
    id: "linuxdo", kind: "linuxdo",
    url: "https://linux.do/tag/2234-tag/2234.rss", hourly: true,
  },
].map((source) => ({ ...source, type: source.kind === "linuxdo" ? "linux.do" as const : "github" as const }));

interface SourceState extends RunState {
  nextAt: number;
  retryAfter?: number;
}
export interface Discovery {
  type: ProjectType;
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
interface RssItem {
  link?: unknown;
  guid?: string | { "#text"?: unknown };
  description?: unknown;
  "content:encoded"?: unknown;
}
function rssItems(xml: string): RssItem[] {
  if (XMLValidator.validate(xml) !== true) throw new Error("RSS invalid XML");
  const parsed = new XMLParser({ ignoreAttributes: false, processEntities: false }).parse(xml);
  if (!parsed.rss || !Object.hasOwn(parsed.rss, "channel"))
    throw new Error("RSS missing channel");
  const items = parsed.rss.channel.item ?? [];
  return Array.isArray(items) ? items : [items];
}
function rssLinks(xml: string): string[][] {
  return rssItems(xml).map((item) => [
    item.link, typeof item.guid === "string" ? item.guid : item.guid?.["#text"],
  ].filter((candidate): candidate is string => typeof candidate === "string"));
}
export function parseLinuxRSS(xml: string): string[] {
  const names = new Map<string, string>();
  for (const item of rssItems(xml)) {
    for (const field of [item.description, item["content:encoded"]]) {
      if (typeof field !== "string") continue;
      for (const name of repositoryLinks(field)) names.set(name.toLowerCase(), name);
    }
  }
  return [...names.values()];
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
function repositoryLinks(text: string): string[] {
  const names = new Map<string, string>();
  for (const match of text.matchAll(/\bhttps?:\/\/github\.com\/([\w.-]+)\/([\w.-]+)(?:\/[^\s<>"'\[\]()，。；：！？]*)?/gi)) {
    // Punctuation ending a prose URL is not part of the repository name.
    const name = repositoryName(match[0].replace(/[.,;:!?]+$/, ""), true);
    if (name) names.set(name.toLowerCase(), name);
  }
  return [...names.values()];
}
export function parseMarkdown(text: string): string[] {
  return repositoryLinks(text).filter((name) => name.split("/")[0].toLowerCase() !== "opengithubs");
}

class LinuxRSSFailure extends Error {
  constructor(message: string, readonly retryAt: number) { super(message); }
}
async function linuxRepositories(upstream: Upstream, primary: string, fallback: string | undefined, deadline: number) {
  if (!fallback) return parseLinuxRSS(await upstream.text(primary, "RSS", {}, deadline));
  const errors: string[] = [];
  const readyAt: number[] = [];
  let attempted = false;
  for (const [role, url] of [["primary", primary], ["fallback", fallback]]) {
    try {
      // Reject a bad optional setting only when it is needed; never expose its value.
      if (!/^https?:$/.test(new URL(url).protocol)) throw new Error("Invalid RSS URL");
      return parseLinuxRSS(await upstream.text(url, "RSS", {}, deadline));
    } catch (error) {
      if (error instanceof DeferredRequest) {
        if (error.retryAt === null) throw error; // Shared discovery budget, not endpoint failure.
        readyAt.push(error.retryAt);
      } else {
        attempted = true;
        // Invalid configuration must not escape the source and break the scheduler.
        try { readyAt.push(upstream.blockedUntil(url)); } catch { readyAt.push(0); }
      }
      errors.push(`${role}: ${safeError(error)}`);
    }
  }
  if (!attempted) throw new DeferredRequest(Math.min(...readyAt));
  throw new LinuxRSSFailure(errors.join("; "), Math.max(upstream.clock.now() + RETRY_DELAY, Math.min(...readyAt)));
}

export async function discover(
  upstream: Upstream,
  state: DiscoveryState,
  now: number,
  prior: RunState | null,
  policy?: SamplingPolicy,
  options: { priorByType?: Partial<Record<ProjectType, RunState>>; linuxdoFallbackUrl?: string } = {},
): Promise<{ entries: Discovery[]; status: RunState; byType: Record<ProjectType, RunState>; state: DiscoveryState; changed: boolean }> {
  const deadline = upstream.clock.now() + DISCOVERY_BUDGET;
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
  const add = (name: string, listedAt: number, type: ProjectType = "github") => {
    const key = `${type}:${name.toLowerCase()}`;
    const previous = entries.get(key);
    if (!previous || previous.listedAt < listedAt)
      entries.set(key, { type, name, listedAt });
  };
  let attempted = false;
  let succeeded = false;
  const attemptedTypes = new Set<ProjectType>();
  const succeededTypes = new Set<ProjectType>();
  const fallback = options.linuxdoFallbackUrl?.trim() || undefined;
  for (const source of SOURCES) {
    const previous = state.sources[source.id];
    // Separate source backoff from the primary's longer host cooldown. For legacy
    // states allow the full three-minute discovery budget before the 30-minute wait.
    const nextAt = source.kind === "linuxdo" && fallback && previous?.failed
      ? Math.min(previous.nextAt, previous.retryAfter ?? previous.attemptedAt + DISCOVERY_BUDGET + RETRY_DELAY) : previous?.nextAt ?? 0;
    if (nextAt > now) continue;
    try {
      let values: string[];
      if (source.kind === "linuxdo") values = await linuxRepositories(upstream, source.url, fallback, deadline);
      else {
        const text = await upstream.text(source.url, source.kind === "markdown" ? "OpenGithubs" : "RSS", {}, deadline);
        values = source.kind === "markdown" ? parseMarkdown(text)
          : source.kind === "github" ? parseGithubRSS(text) : parseHelloRSS(text);
      }
      attempted = true;
      attemptedTypes.add(source.type);
      if (source.kind === "hellogithub") {
        for (const url of new Set(values)) {
          const cached = state.resolved[url];
          if (cached) add(cached.name, now);
          else if (!state.details.some((task) => task.url === url))
            state.details.push({ url, listedAt: now, nextAt: now, error: null });
        }
      } else for (const name of values) add(name, now, source.type);
      state.sources[source.id] = {
        attemptedAt: now, succeededAt: now, succeeded: values.length, failed: 0, error: null,
        nextAt: nextSlot(upstream.clock.now(), source.hourly, policy),
      };
      succeeded = true;
      succeededTypes.add(source.type);
    } catch (error) {
      if (error instanceof DeferredRequest) {
        if (error.retryAt === null) break;
        continue;
      }
      attempted = true;
      attemptedTypes.add(source.type);
      state.sources[source.id] = {
        attemptedAt: now, succeededAt: previous?.succeededAt ?? null,
        succeeded: 0, failed: 1, error: error instanceof LinuxRSSFailure ? error.message : safeError(error),
        retryAfter: upstream.clock.now() + RETRY_DELAY,
        nextAt: error instanceof LinuxRSSFailure ? error.retryAt
          : Math.max(upstream.clock.now() + RETRY_DELAY, upstream.blockedUntil(source.url)),
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
      attemptedTypes.add("github");
      const name = parseHelloProject(text);
      add(name, task.listedAt);
      state.resolved[task.url] = { day: beijingDay(upstream.clock.now()), name };
      state.details.splice(state.details.indexOf(task), 1);
      succeeded = true;
      succeededTypes.add("github");
    } catch (error) {
      if (error instanceof DeferredRequest) {
        detailRequests--;
        if (error.retryAt === null) break;
        continue;
      }
      attempted = true;
      attemptedTypes.add("github");
      task.error = safeError(error);
      task.nextAt = Math.max(upstream.clock.now() + RETRY_DELAY, upstream.blockedUntil(task.url));
    }
  }
  const errors = [
    ...Object.entries(state.sources).flatMap(([id, value]) => value.failed ? [`${id}: ${value.error}`] : []),
    ...state.details.flatMap((task) => task.error ? [`hellogithub-detail: ${task.error}`] : []),
  ];
  return {
    entries: [...entries.values()], state, changed: changed || attempted,
    byType: Object.fromEntries(PROJECT_TYPES.map((type) => {
      const sources = SOURCES.filter((source) => source.type === type)
        .flatMap((source) => state.sources[source.id] ? [[source.id, state.sources[source.id]] as const] : []);
      const errors = [
        ...sources.flatMap(([id, value]) => value.failed ? [`${id}: ${value.error}`] : []),
        ...(type === "github" ? state.details.flatMap((task) => task.error ? [`hellogithub-detail: ${task.error}`] : []) : []),
      ];
      const previous = options.priorByType?.[type];
      return [type, {
        attemptedAt: attemptedTypes.has(type) ? now : previous?.attemptedAt ?? Math.max(0, ...sources.map(([, value]) => value.attemptedAt)),
        succeededAt: succeededTypes.has(type) ? now : previous?.succeededAt ?? (Math.max(0, ...sources.map(([, value]) => value.succeededAt ?? 0)) || null),
        succeeded: attemptedTypes.has(type) ? [...entries.values()].filter((entry) => entry.type === type).length : previous?.succeeded ?? 0,
        failed: errors.length, error: errors[0] ?? null,
      }];
    })) as Record<ProjectType, RunState>,
    status: {
      attemptedAt: attempted ? now : prior?.attemptedAt ?? now,
      succeededAt: succeeded ? now : prior?.succeededAt ?? null,
      succeeded: attempted ? entries.size : prior?.succeeded ?? 0,
      failed: errors.length, error: errors[0] ?? null,
    },
  };
}
