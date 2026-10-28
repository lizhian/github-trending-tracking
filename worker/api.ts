import {
  WINDOWS,
  PROJECT_TYPES,
  type ProjectType,
  type ListResult,
  type Project,
  type WindowHours,
} from "./contracts";
import { histories, projects as projectViews, samplingPolicy, type ProjectRow } from "./store";
import { windowTrend } from "./trends";
import { isStale } from "./schedule";
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
    "type",
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
  const type = (url.searchParams.get("type") ?? "github") as ProjectType;
  if (
    !["name", "stars", ...WINDOWS.map(String)].includes(sort) ||
    !["asc", "desc"].includes(direction) ||
    !/^[1-9]\d{0,5}$/.test(pageText) || !PROJECT_TYPES.includes(type)
  )
    throw new ParameterError("排序或页码无效");
  return {
    q,
    language,
    topic,
    sort,
    direction,
    page: Number(pageText),
    type,
  };
}
async function list(env: Env, url: URL, now: number): Promise<ListResult> {
  const {
    q,
    language,
    topic,
    sort,
    direction,
    page: requested,
    type,
  } = parameters(url);
  const rows = (
    await env.DB.prepare("SELECT * FROM projects WHERE type=?").bind(type).all<ProjectRow>()
  ).results;
  const projects = await projectViews(env.DB, rows, now);
  const languages = [
    ...new Set(projects.flatMap((p) => (p.language ? [p.language] : []))),
  ].sort();
  const topics = [...new Set(projects.flatMap((p) => p.topics))].sort();
  const comparable = projects.filter((p) => p.trends[24].delta !== null);
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
  const pageSize = 100;
  const pages = Math.max(1, Math.ceil(filtered.length / pageSize));
  const page = Math.min(requested, pages);
  const states = (
    await env.DB.prepare(
      "SELECT key,value FROM system_state WHERE key IN ('scheduler',?,?,?,'discovery','collection','cleanup')",
    ).bind(`discovery:${type}`, `collection:${type}`, `cleanup:${type}`).all<{ key: string; value: string }>()
  ).results;
  const status: ListResult["status"] = {};
  for (const key of ["discovery", "collection", "cleanup", "scheduler"]) {
    const row = states.find((row) => row.key === `${key}:${type}`)
      ?? (key === "scheduler" || type === "github" ? states.find((row) => row.key === key) : undefined);
    if (row) status[key] = JSON.parse(row.value);
  }
  return {
    items: filtered.slice((page - 1) * pageSize, page * pageSize),
    total: filtered.length,
    page,
    pageSize,
    pages,
    languages,
    topics,
    summary: {
      tracked: projects.length,
      fresh: projects.filter((p) => !p.stale).length,
      accumulating: projects.filter((p) => p.trends[24].delta === null).length,
      net24: comparable.length
        ? comparable.reduce((sum, p) => sum + p.trends[24].delta!, 0)
        : null,
      comparable24: comparable.length,
    },
    status,
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
    "Cache-Control": "no-store",
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
      const cacheUrl = new URL(url);
      cacheUrl.searchParams.set("_schema", "tracking-types-v3");
      const cacheKey = new Request(cacheUrl.toString());
      const cached = await cache?.match(cacheKey);
      if (cached) {
        const response = new Response(cached.body, cached);
        response.headers.set("Cache-Control", "no-store");
        response.headers.delete("Access-Control-Allow-Origin");
        if (origin) response.headers.set("Access-Control-Allow-Origin", origin);
        return response;
      }
      const response = json(await list(env, url, now));
      if (cache) {
        // Cache only within this Worker. Zone browser-TTL settings must not
        // extend the visible age beyond the ten-minute observation snapshot.
        const stored = response.clone();
        stored.headers.set("Cache-Control", "public, max-age=600");
        await cache.put(cacheKey, stored);
      }
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
      const policy = await samplingPolicy(env.DB);
      const trend = windowTrend(points, hours, now, undefined, policy);
      headers.set("Cache-Control", "no-store");
      return json({
        points: trend.points,
        trend,
        gaps: trend.gaps,
        observedAt: row.observed_at,
        stale: isStale(row.observed_at, now, policy),
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
