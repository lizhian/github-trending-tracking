import { WINDOWS, type ListResult, type WindowHours } from "./contracts";
import { histories, project, type ProjectRow } from "./store";
import { allTrends, HOUR, RETENTION, windowTrend } from "./trends";
import type { Env } from "./index";

class ParameterError extends Error {}
function parameters(url: URL) {
  const allowed = new Set([
    "q",
    "language",
    "topic",
    "sort",
    "direction",
    "page",
  ]);
  for (const [key, value] of url.searchParams) {
    if (
      !allowed.has(key) ||
      value.length > 200 ||
      url.searchParams.getAll(key).length !== 1
    )
      throw new ParameterError("查询参数无效");
  }
  const q = url.searchParams.get("q")?.trim().toLowerCase() ?? "";
  const language = url.searchParams.get("language") ?? "";
  const topic = url.searchParams.get("topic") ?? "";
  const sort = url.searchParams.get("sort") ?? "24";
  const direction = url.searchParams.get("direction") ?? "desc";
  const pageText = url.searchParams.get("page") ?? "1";
  if (
    !["name", "stars", ...WINDOWS.map(String)].includes(sort) ||
    !["asc", "desc"].includes(direction) ||
    !/^[1-9]\d{0,5}$/.test(pageText)
  )
    throw new ParameterError("排序或页码无效");
  return { q, language, topic, sort, direction, page: Number(pageText) };
}
async function list(env: Env, url: URL, now: number): Promise<ListResult> {
  const {
    q,
    language,
    topic,
    sort,
    direction,
    page: requested,
  } = parameters(url);
  const rows = (
    await env.DB.prepare("SELECT * FROM projects").all<ProjectRow>()
  ).results;
  // Only summaries are scanned; full hourly histories use the cycle/day primary key.
  // Load exact history only when the rolling retention boundary invalidates a summary.
  const expired = rows.filter((row) => {
    const trend = JSON.parse(row.trends)["720"];
    return (
      trend.start !== null &&
      trend.start < now - RETENTION &&
      now - row.observed_at > 2 * HOUR
    );
  });
  const expiredCycles = new Set(expired.map((row) => row.cycle));
  const actual = await histories(env.DB, [...expiredCycles], now);
  const projects = rows.map((row) => {
    const item = project(row, now);
    if (expiredCycles.has(row.cycle))
      item.trends = allTrends(actual.get(row.cycle) ?? [], now);
    else {
      // Summaries keep the first four actual hours. A fresh (<=2h) summary can
      // cross at most two of those hours at the 30-day boundary.
      const trend = item.trends[720];
      if (trend.start !== null && trend.start < now - RETENTION) {
        item.trends[720] = windowTrend(trend.points, 720, now, trend.gaps);
      }
    }
    return item;
  });
  const languages = [
    ...new Set(projects.flatMap((p) => (p.language ? [p.language] : []))),
  ].sort();
  const topics = [...new Set(projects.flatMap((p) => p.topics))].sort();
  const filtered = projects.filter(
    (p) =>
      (!q || `${p.name} ${p.description ?? ""}`.toLowerCase().includes(q)) &&
      (!language || p.language === language) &&
      (!topic || p.topics.includes(topic)),
  );
  filtered.sort((a, b) => {
    const av =
      sort === "name"
        ? a.name.toLowerCase()
        : sort === "stars"
          ? a.stars
          : a.trends[Number(sort) as WindowHours].delta;
    const bv =
      sort === "name"
        ? b.name.toLowerCase()
        : sort === "stars"
          ? b.stars
          : b.trends[Number(sort) as WindowHours].delta;
    if (av === null || bv === null)
      return av === bv
        ? a.name.localeCompare(b.name, "en")
        : av === null
          ? 1
          : -1;
    const comparison =
      typeof av === "string" && typeof bv === "string"
        ? av.localeCompare(bv, "en")
        : Number(av) - Number(bv);
    return (
      comparison * (direction === "asc" ? 1 : -1) ||
      a.name.localeCompare(b.name, "en")
    );
  });
  const pages = Math.max(1, Math.ceil(filtered.length / 50));
  const page = Math.min(requested, pages);
  const states = (
    await env.DB.prepare(
      "SELECT key,value FROM system_state WHERE key IN ('discovery','collection','cleanup','scheduler')",
    ).all<{ key: string; value: string }>()
  ).results;
  return {
    items: filtered.slice((page - 1) * 50, page * 50),
    total: filtered.length,
    page,
    pageSize: 50,
    pages,
    languages,
    topics,
    summary: {
      tracked: projects.length,
      fresh: projects.filter((p) => !p.stale).length,
      accumulating: projects.filter((p) => p.trends[24].delta === null).length,
    },
    status: Object.fromEntries(
      states.map((row) => [row.key, JSON.parse(row.value)]),
    ),
    asOf: now,
  };
}
export async function query(request: Request, env: Env): Promise<Response> {
  const origin = request.headers.get("Origin");
  if (origin && origin !== env.ALLOWED_ORIGIN)
    return Response.json({ error: "来源不被允许" }, { status: 403 });
  const headers = new Headers({
    Vary: "Origin",
    "X-Content-Type-Options": "nosniff",
  });
  if (origin) headers.set("Access-Control-Allow-Origin", origin);
  const json = (value: unknown, status = 200) =>
    Response.json(value, { status, headers });
  if (!["GET", "OPTIONS"].includes(request.method)) {
    headers.set("Allow", "GET, OPTIONS");
    return json({ error: "仅支持只读查询" }, 405);
  }
  if (request.method === "OPTIONS") {
    headers.set("Access-Control-Allow-Methods", "GET, OPTIONS");
    return new Response(null, { status: 204, headers });
  }
  const url = new URL(request.url);
  const now = Date.now();
  try {
    if (url.pathname === "/api/projects") {
      parameters(url); // Reject invalid queries even if a cached key exists.
      const cache =
        typeof caches !== "undefined"
          ? (caches as CacheStorage & { default: Cache }).default
          : undefined;
      const cacheKey = new Request(url.toString());
      const cached = await cache?.match(cacheKey);
      if (cached) {
        const response = new Response(cached.body, cached);
        response.headers.delete("Access-Control-Allow-Origin");
        if (origin) response.headers.set("Access-Control-Allow-Origin", origin);
        return response;
      }
      headers.set("Cache-Control", "public, max-age=60");
      const response = json(await list(env, url, now));
      if (cache) await cache.put(cacheKey, response.clone());
      return response;
    }
    const match = url.pathname.match(/^\/api\/projects\/([^/]+)\/history$/);
    if (match) {
      if (
        [...url.searchParams.keys()].some((key) => key !== "window") ||
        url.searchParams.getAll("window").length > 1
      )
        throw new ParameterError("趋势窗口无效");
      const hours = Number(url.searchParams.get("window") ?? 24) as WindowHours;
      if (!WINDOWS.includes(hours)) throw new ParameterError("趋势窗口无效");
      const row = await env.DB.prepare("SELECT * FROM projects WHERE id=?")
        .bind(decodeURIComponent(match[1]))
        .first<ProjectRow>();
      if (!row) return json({ error: "项目已淘汰或不存在，请刷新列表" }, 404);
      const points =
        (await histories(env.DB, [row.cycle], now)).get(row.cycle) ?? [];
      const trend = windowTrend(points, hours, now);
      headers.set("Cache-Control", "no-store");
      return json({
        points: trend.points,
        trend,
        gaps: trend.gaps,
        observedAt: row.observed_at,
        stale: now - row.observed_at > 2 * HOUR,
      });
    }
    return json({ error: "接口不存在" }, 404);
  } catch (error) {
    if (error instanceof ParameterError || error instanceof URIError)
      return json(
        {
          error:
            error instanceof ParameterError ? error.message : "项目标识无效",
        },
        400,
      );
    return json({ error: "数据暂时不可用，请稍后重试" }, 503);
  }
}
