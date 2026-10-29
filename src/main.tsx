import React, {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createRoot } from "react-dom/client";
import {
  WINDOWS,
  type HistoryResult,
  type ListResult,
  type Point,
  type Project,
  type RunState,
  type Trend,
  type TrendSummary,
  type WindowHours,
} from "../shared/contracts";
import { SearchSelect } from "./SearchSelect";
import "./style.css";

const labels: Record<WindowHours, string> = {
  24: "24 小时",
  72: "3 天",
  168: "7 天",
  720: "30 天",
};
const number = (value: number) => new Intl.NumberFormat("zh-CN").format(value);
const signed = (value: number | null) =>
  value === null
    ? "积累中"
    : `${value < 0 ? "−" : "+"}${number(Math.abs(value))}`;
const compact = (value: number) =>
  Math.abs(value) > 1000 ? `${(value / 1000).toFixed(1)}k` : number(value);
const compactSigned = (value: number | null) =>
  value === null
    ? "积累中"
    : `${value < 0 ? "−" : "+"}${compact(Math.abs(value))}`;
const time = (value: number | null | undefined) =>
  value
    ? new Date(value).toLocaleString("zh-CN", { hour12: false })
    : "尚无成功记录";
const stateLabel = (trend: TrendSummary) =>
  trend.state === "accumulating"
    ? "积累中"
    : trend.state === "partial"
      ? "部分观测"
      : "完整观测";
const apiBase = (import.meta.env.VITE_API_URL ?? "").replace(/\/$/, "");
const sorts = ["name", "stars", ...WINDOWS.map(String)];
interface QueryState {
  q: string;
  language: string;
  topic: string;
  tab: "github" | "linuxdo";
  sort: string;
  direction: "asc" | "desc";
  page: number;
  project: string;
  window: WindowHours;
}
function readLocation(): QueryState {
  const params = new URLSearchParams(location.search);
  const text = (key: string) => (params.get(key) ?? "").slice(0, 200);
  const page = text("page");
  const hours = Number(text("window")) as WindowHours;
  return {
    q: text("q"),
    language: text("language"),
    topic: text("topic"),
    tab: text("tab") === "linuxdo" ? "linuxdo" : "github",
    sort: sorts.includes(text("sort")) ? text("sort") : "24",
    direction: text("direction") === "asc" ? "asc" : "desc",
    page: /^[1-9]\d{0,5}$/.test(page) ? Number(page) : 1,
    project: text("project"),
    window: WINDOWS.includes(hours) ? hours : 720,
  };
}
function listParameters(query: QueryState) {
  return new URLSearchParams({
    type: query.tab === "linuxdo" ? "linux.do" : "github",
    q: query.q,
    language: query.language,
    topic: query.topic,
    sort: query.sort,
    direction: query.direction,
    page: String(query.page),
  });
}
function locationFor(query: QueryState) {
  const params = listParameters(query);
  params.delete("type");
  for (const [key, value] of [...params]) {
    if (
      !value ||
      (key === "sort" && value === "24") ||
      (key === "direction" && value === "desc") ||
      (key === "page" && value === "1")
    )
      params.delete(key);
  }
  if (query.tab === "linuxdo") params.set("tab", "linuxdo");
  if (query.project) params.set("project", query.project);
  if (query.window !== 720) params.set("window", String(query.window));
  return `${location.pathname}${params.size ? `?${params}` : ""}${location.hash}`;
}
interface Resource<T> {
  path: string;
  value: T | null;
  loading: boolean;
  error: string;
}
function useApi<T>(
  path: string,
  revision: string | number = 0,
  enabled = true,
): Resource<T> {
  const [resource, setResource] = useState<Resource<T>>({
    path: "",
    value: null,
    loading: true,
    error: "",
  });
  useEffect(() => {
    if (!enabled) return;
    const controller = new AbortController();
    setResource((previous) => ({
      path,
      value: previous.path === path ? previous.value : null,
      loading: true,
      error: "",
    }));
    async function read() {
      try {
        const response = await fetch(`${apiBase}${path}`, {
          signal: controller.signal,
          cache: "no-store",
        });
        if (!response.ok) {
          const body = (await response.json().catch(() => null)) as {
            error?: string;
          } | null;
          throw new Error(body?.error || "请求失败，请稍后重试");
        }
        const value = (await response.json()) as T;
        if (!controller.signal.aborted)
          setResource({ path, value, loading: false, error: "" });
      } catch (error) {
        if (!controller.signal.aborted)
          setResource((previous) => ({
            path,
            value: previous.path === path ? previous.value : null,
            loading: false,
            error:
              error instanceof Error ? error.message : "请求失败，请稍后重试",
          }));
      }
    }
    void read();
    return () => controller.abort();
  }, [path, revision, enabled]);
  if (!enabled) return { ...resource, loading: false, error: "" };
  return resource.path === path
    ? resource
    : { path, value: null, loading: true, error: "" };
}
function Icon({
  name,
  size = 16,
}: {
  name:
    | "mark"
    | "search"
    | "refresh"
    | "reset"
    | "github"
    | "external"
    | "previous"
    | "next"
    | "sun"
    | "moon";
  size?: number;
}) {
  const paths = {
    mark: (
      <>
        <path d="M3 21V3h6v18M15 21V3h6v18M3 12h18" />
        <path className="mark-signal" d="m6 18 6-6 6-6" />
      </>
    ),
    search: (
      <>
        <circle cx="10.5" cy="10.5" r="6.5" />
        <path d="m16 16 5 5" />
      </>
    ),
    refresh: (
      <>
        <path d="M20 7v5h-5M4 17v-5h5" />
        <path d="M6 6a8 8 0 0 1 14 6M4 12a8 8 0 0 0 14 6" />
      </>
    ),
    reset: (
      <>
        <path d="M4 10a8 8 0 1 1 1 8M4 4v6h6" />
      </>
    ),
    github: (
      <path d="M9 21v-4c-4 1-4-2-6-2m12 6v-4a3.5 3.5 0 0 0-1-3c3-.4 6-1.5 6-6a4.7 4.7 0 0 0-1-3c.1-1 .1-2-1-3-2 0-3 1-3 1a12 12 0 0 0-6 0s-1-1-3-1c-1 1-1 2-1 3a4.7 4.7 0 0 0-1 3c0 4.5 3 5.6 6 6a3.5 3.5 0 0 0-1 3" />
    ),
    external: <path d="M14 3h7v7M21 3 10 14M10 3H3v18h18v-7" />,
    previous: <path d="m15 5-7 7 7 7" />,
    next: <path d="m9 5 7 7-7 7" />,
    sun: (
      <>
        <circle cx="12" cy="12" r="4" />
        <path d="M12 2v2m0 16v2M2 12h2m16 0h2M5 5l2 2m10 10 2 2M5 19l2-2M17 7l2-2" />
      </>
    ),
    moon: <path d="M20.5 13.5A9 9 0 0 1 10.5 3a9 9 0 1 0 10 10.5Z" />,
  };
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {paths[name]}
    </svg>
  );
}
function ChartPlaceholder() {
  return (
    <div className="chart-placeholder">
      <svg
        className="chart chart-skeleton"
        viewBox="0 0 288 145"
        aria-hidden="true"
      >
        {[29, 67.5, 106].map((y) => (
          <g key={y}>
            <line className="chart-grid" x1="4" x2="232" y1={y} y2={y} />
            <rect
              className="skeleton-mark"
              x="240"
              y={y - 3}
              width="28"
              height="6"
              rx="2"
            />
          </g>
        ))}
        <rect
          className="skeleton-mark"
          x="4"
          y="129"
          width="32"
          height="6"
          rx="2"
        />
        <rect
          className="skeleton-mark"
          x="200"
          y="129"
          width="32"
          height="6"
          rx="2"
        />
      </svg>
      <p className="chart-loading-label" role="status">
        正在加载趋势
      </p>
    </div>
  );
}
function Chart({ trend }: { trend: Trend }) {
  const points = trend.points;
  const min = Math.min(...points.map((point) => point[1]));
  const max = Math.max(...points.map((point) => point[1]));
  const ticks = [max, (max + min) / 2, min].map(Math.round);
  const distinct = [...new Set(ticks)];
  const format = (value: number, precision: number) =>
    value > 1000 ? `${(value / 1000).toFixed(precision)}k` : number(value);
  let precision = 1;
  while (
    precision < 3 &&
    new Set(distinct.map((value) => format(value, precision))).size <
      distinct.length
  )
    precision++;
  const tickLabels = ticks.map((value) => format(value, precision));
  const viewWidth = Math.max(
    288,
    248 + Math.max(...tickLabels.map((label) => label.length)) * 7,
  );
  const first = points[0]?.[0] ?? 0;
  const last = points.at(-1)?.[0] ?? first;
  const range = max - min || 1;
  const xy = ([at, stars]: Point) => [
    first === last ? 232 : 4 + ((at - first) / (last - first)) * 228,
    min === max ? 67.5 : 106 - ((stars - min) / range) * 77,
  ];
  const segments: Point[][] = [];
  for (const point of points) {
    const previous = segments.at(-1)?.at(-1);
    if (
      !previous ||
      trend.gaps.some(([from, to]) => previous[0] <= from && point[0] >= to)
    )
      segments.push([point]);
    else segments.at(-1)!.push(point);
  }
  return (
    <svg
      className="chart"
      viewBox={`0 0 ${viewWidth} 145`}
      role="img"
      aria-label={`Star 总数曲线，${stateLabel(trend)}，净增量 ${signed(trend.delta)}，${trend.gaps.length} 段缺测`}
    >
      {[29, 67.5, 106].map((y, index) => (
        <g key={y}>
          <line className="chart-grid" x1="4" x2="232" y1={y} y2={y} />
          {(min === max ? index === 1 : ticks.indexOf(ticks[index]) === index) && (
            <text className="chart-tick" x="240" y={y + 3}>
              <title>{number(ticks[index])} Star</title>
              {tickLabels[index]}
            </text>
          )}
        </g>
      ))}
      {segments.map((segment, index) => (
        <polyline
          key={index}
          className="chart-line"
          points={segment.map((point) => xy(point).join(",")).join(" ")}
        />
      ))}
      {points.map((point, index) => {
        const [cx, cy] = xy(point);
        return (
          <circle
            key={index}
            className="chart-point"
            cx={cx}
            cy={cy}
            r={points.length > 48 ? 1.5 : 2.5}
          >
            <title>
              {time(point[0])}：{number(point[1])} Star
            </title>
          </circle>
        );
      })}
      <text x="4" y="132">
        {new Date(first).toLocaleDateString("zh-CN", {
          month: "numeric",
          day: "numeric",
        })}
      </text>
      <text x="232" y="132" textAnchor="end">
        最新快照
      </text>
    </svg>
  );
}
function ProjectColumns() {
  return (
    <colgroup>
      <col className="rank-column" />
      <col className="name-column" />
      <col className="language-column" />
      {Array.from({ length: 5 }, (_, index) => (
        <col className="number-column" key={index} />
      ))}
    </colgroup>
  );
}
function TrendValue({ trend }: { trend: TrendSummary }) {
  const hint = `${stateLabel(trend)}${trend.start && trend.end ? `，${time(trend.start)} 至 ${time(trend.end)}` : ""}${trend.gaps.length ? `，${trend.gaps.length} 段缺测` : ""}`;
  return (
    <span
      className={
        trend.delta === null
          ? "accumulating"
          : `gain ${trend.delta < 0 ? "negative" : ""}`
      }
      title={`${signed(trend.delta)}，${hint}`}
      aria-label={`${signed(trend.delta)}，${hint}`}
    >
      {compactSigned(trend.delta)}
    </span>
  );
}
function Details({
  project,
  window,
  refresh,
  asOf,
  onWindow,
  onTopic,
  onRefresh,
}: {
  project: Project;
  window: WindowHours;
  refresh: number;
  asOf: number;
  onWindow: (hours: WindowHours) => void;
  onTopic: (topic: string) => void;
  onRefresh: () => void;
}) {
  const [retry, setRetry] = useState(0);
  const history = useApi<HistoryResult>(
    `/api/projects/${encodeURIComponent(project.id)}/history?window=${window}`,
    `${project.observedAt}:${refresh}:${retry}`,
  );
  const current = history.value;
  const trend = current?.trend;
  const [owner, ...name] = project.name.split("/");
  const protectedNow = asOf - project.enteredAt < 24 * 3_600_000;
  const days =
    trend?.start && trend.end ? (trend.end - trend.start) / 86_400_000 : null;
  return (
    <section className="detail-content" aria-label={`${project.name} 详情`}>
      <h2 className="detail-title">
        <a
          href={`https://github.com/${project.name}`}
          target="_blank"
          rel="noreferrer"
        >
          <span className="detail-owner">{owner} / </span>
          <wbr />
          <span className="detail-repo-name">{name.join("/")}</span>
        </a>
      </h2>
      <p className="detail-description">
        {project.description || "仓库尚未提供简介。"}
      </p>
      <div className="topics" aria-label="仓库 Topics">
        {project.topics.length ? (
          project.topics.map((topic) => (
            <button
              type="button"
              className="topic-button"
              key={topic}
              onClick={() => onTopic(topic)}
              aria-label={`筛选 Topic ${topic}`}
            >
              {topic}
            </button>
          ))
        ) : (
          <span className="detail-muted">仓库尚未提供 Topics</span>
        )}
      </div>
      {(project.stale || project.error) && (
        <p className="notice" role="status">
          {project.stale ? "数据过期，未在应采时段及 10 分钟宽限内成功更新。" : ""}
          {project.error ? "项目更新异常，继续显示有效观测。" : ""}
        </p>
      )}
      <div className="detail-stat">
        <span>Star 总数</span>
        <div className="detail-star-line">
          <strong>{number(project.stars)}</strong>
          {trend && !history.loading && (
            <span
              className={`delta-badge ${trend.delta !== null && trend.delta < 0 ? "negative" : ""}`}
            >
              {signed(trend.delta)}
            </span>
          )}
        </div>
      </div>
      <div className="history-region" aria-busy={history.loading}>
        <div className="chart-heading">
          <span>Star 总数</span>
          <span>局部纵轴</span>
        </div>
        <div className="chart-stage">
          {history.loading ? (
            <ChartPlaceholder />
          ) : history.error ? (
            <div className="chart-message" role="alert">
              <p>{history.error}</p>
              <button
                type="button"
                onClick={() => setRetry((value) => value + 1)}
              >
                重试历史查询
              </button>
              <button type="button" onClick={onRefresh}>
                刷新项目列表
              </button>
            </div>
          ) : trend && current ? (
            trend.points.length ? (
              <Chart trend={trend} />
            ) : (
              <p className="chart-message">积累中</p>
            )
          ) : null}
        </div>
        <div className="chart-feedback">
          {!history.loading &&
            !history.error &&
            current &&
            (current.gaps.length > 0 || current.stale) && (
              <p className="chart-note" role="status">
                {current.gaps.length ? `缺测 ${current.gaps.length} 段` : ""}
                {current.stale ? " 历史数据过期" : ""}
              </p>
            )}
        </div>
      </div>
      <div className="periods" aria-label="趋势窗口">
        {WINDOWS.map((hours) => (
          <button
            type="button"
            key={hours}
            aria-pressed={window === hours}
            onClick={() => onWindow(hours)}
          >
            {labels[hours]}
          </button>
        ))}
      </div>
      <dl className="observation">
        <dt>编程语言</dt>
        <dd>{project.language || "未提供"}</dd>
        <dt>{labels[window]} Star 净增量</dt>
        <dd>
          {history.loading
            ? "读取中"
            : trend
              ? signed(trend.delta)
              : "暂不可用"}
        </dd>
        <dt>保护期</dt>
        <dd>{protectedNow ? "保护中" : "已结束"}</dd>
      </dl>
      <details className="record-times">
        <summary>观测详情</summary>
        <dl className="observation">
          <dt>观测状态</dt>
          <dd>
            {history.loading
              ? "读取中"
              : trend
                ? stateLabel(trend)
                : "查询失败"}
          </dd>
          <dt>实际观测跨度</dt>
          <dd>
            {days === null
              ? "尚无有效观测"
              : days < 1
                ? `${Math.round(days * 24 * 10) / 10} 小时`
                : `${Math.round(days * 10) / 10} 天`}
          </dd>
          <dt>实际观测次数</dt>
          <dd>
            {history.loading
              ? "读取中"
              : current
                ? current.points.length
                : "暂不可用"}
          </dd>
          <dt>本次入库</dt>
          <dd>{time(project.enteredAt)}</dd>
          <dt>最近上榜</dt>
          <dd>{time(project.listedAt)}</dd>
          <dt>最后成功观测</dt>
          <dd>{time(project.observedAt)}</dd>
        </dl>
        {trend?.start && trend.end && !history.loading ? (
          <p className="actual-interval">
            实际区间：{time(trend.start)} 至 {time(trend.end)}
          </p>
        ) : null}
      </details>
      <a
        className="repo-link"
        href={`https://github.com/${project.name}`}
        target="_blank"
        rel="noreferrer"
      >
        <span>在 GitHub 查看项目</span>
        <Icon name="external" size={14} />
      </a>
    </section>
  );
}
function App() {
  const [query, setQuery] = useState(readLocation);
  const [draft, setDraft] = useState(query.q);
  const [refresh, setRefresh] = useState(0);
  const [dark, setDark] = useState(() => {
    try {
      return localStorage.getItem("theme") === "dark";
    } catch {
      return false;
    }
  });
  const pushNext = useRef(false);
  const controls = useRef<HTMLDivElement>(null);
  const tableScroll = useRef<HTMLDivElement>(null);
  const headerScroll = useRef<HTMLDivElement>(null);
  const inspector = useRef<HTMLElement>(null);
  const masthead = useRef<HTMLElement>(null);
  const viewTabs = useRef<HTMLDivElement>(null);
  const [indicator, setIndicator] = useState({ x: 0, width: 0 });
  const searchInput = useRef<HTMLInputElement>(null);
  const commit = useCallback((patch: Partial<QueryState>, push = true) => {
    pushNext.current = push;
    setQuery((previous) => ({ ...previous, ...patch }));
  }, []);
  const listPath = `/api/projects?${listParameters(query)}`;
  const previousListPath = useRef(listPath);
  const resource = useApi<ListResult>(listPath, refresh);
  const data = resource.value;
  const options = data;
  const selected =
    data?.items.find((project) => project.id === query.project) ??
    data?.items[0];
  useEffect(() => {
    const next = locationFor(query);
    if (next !== `${location.pathname}${location.search}${location.hash}`) {
      if (pushNext.current) history.pushState(null, "", next);
      else history.replaceState(null, "", next);
    }
    pushNext.current = false;
  }, [query]);
  useEffect(() => {
    const restore = () => {
      const next = readLocation();
      pushNext.current = false;
      setDraft(next.q);
      setQuery(next);
    };
    addEventListener("popstate", restore);
    return () => removeEventListener("popstate", restore);
  }, []);
  useEffect(() => {
    if (draft === query.q) return;
    const timer = setTimeout(() => commit({ q: draft, page: 1 }), 300);
    return () => clearTimeout(timer);
  }, [draft, query.q, commit]);
  useEffect(() => {
    if (!data || resource.loading || resource.error)
      return;
    const project = selected?.id ?? "";
    if (query.page !== data.page || query.project !== project)
      commit({ page: data.page, project }, false);
  }, [
    data,
    resource.loading,
    resource.error,
    selected?.id,
    query.page,
    query.project,
    query.tab,
    commit,
  ]);
  useEffect(() => {
    document.documentElement.dataset.theme = dark ? "dark" : "light";
    try {
      localStorage.setItem("theme", dark ? "dark" : "light");
    } catch {
      /* Preference storage is optional. */
    }
  }, [dark]);
  useLayoutEffect(() => {
    if (inspector.current) inspector.current.scrollTop = 0;
  }, [selected?.id]);
  useLayoutEffect(() => {
    if (previousListPath.current === listPath) return;
    previousListPath.current = listPath;
    if (tableScroll.current) tableScroll.current.scrollTop = 0;
    if (matchMedia("(max-width:760px)").matches)
      controls.current?.scrollIntoView({ block: "start", behavior: "instant" });
  }, [listPath]);
  useLayoutEffect(() => {
    const alignHeader = () => {
      const viewport = tableScroll.current;
      if (viewport && headerScroll.current) {
        const style = getComputedStyle(viewport);
        const width =
          viewport.clientWidth -
          parseFloat(style.paddingInlineStart) -
          parseFloat(style.paddingInlineEnd);
        headerScroll.current.style.width = `${Math.max(0, width)}px`;
      }
    };
    const observer = new ResizeObserver(alignHeader);
    if (tableScroll.current) observer.observe(tableScroll.current);
    alignHeader();
    return () => observer.disconnect();
  }, [query.tab]);
  useLayoutEffect(() => {
    const header = masthead.current;
    if (!header) return;
    const measure = () =>
      document.documentElement.style.setProperty(
        "--masthead-height",
        `${header.getBoundingClientRect().height}px`,
      );
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(header);
    return () => observer.disconnect();
  }, []);
  useLayoutEffect(() => {
    const tabs = viewTabs.current;
    if (!tabs) return;
    const measure = () => {
      const active = tabs.querySelector<HTMLElement>('[aria-pressed="true"]');
      if (active)
        setIndicator({ x: active.offsetLeft, width: active.offsetWidth });
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(tabs);
    for (const button of tabs.querySelectorAll("button"))
      observer.observe(button);
    return () => observer.disconnect();
  }, [query.tab]);
  useEffect(() => {
    const focusSearch = (event: KeyboardEvent) => {
      if (
        event.key === "/" &&
        !event.ctrlKey &&
        !event.metaKey &&
        !/input|select|textarea/i.test((event.target as HTMLElement).tagName) &&
        !(event.target as HTMLElement).isContentEditable
      ) {
        event.preventDefault();
        searchInput.current?.focus();
      }
    };
    addEventListener("keydown", focusSearch);
    return () => removeEventListener("keydown", focusSearch);
  }, []);
  const clear = () => {
    setDraft("");
    commit({ q: "", language: "", topic: "", page: 1 });
  };
  const changeSort = (sort: string) =>
    commit({
      sort,
      direction:
        query.sort === sort && query.direction === "desc" ? "asc" : "desc",
      page: 1,
    });
  const selectProject = (project: Project) => {
    commit({ project: project.id });
    if (inspector.current) inspector.current.scrollTop = 0;
    if (matchMedia("(max-width:760px)").matches)
      requestAnimationFrame(() =>
        inspector.current?.scrollIntoView({
          block: "start",
          behavior: "instant",
        }),
      );
  };
  const syncHorizontal = (
    source: HTMLDivElement,
    target: HTMLDivElement | null,
  ) => {
    if (target && target.scrollLeft !== source.scrollLeft)
      target.scrollLeft = source.scrollLeft;
  };
  const sortHeading = (field: string, label: string, className = "numeric") => (
    <th
      scope="col"
      className={className}
      aria-sort={
        query.sort === field
          ? query.direction === "desc"
            ? "descending"
            : "ascending"
          : "none"
      }
    >
      <button type="button" onClick={() => changeSort(field)}>
        {label}
        {query.sort === field && (
          <span aria-hidden="true">
            {" "}
            {query.direction === "desc" ? "↓" : "↑"}
          </span>
        )}
      </button>
    </th>
  );
  const collection = data?.status.collection as RunState | undefined;
  const failures = [
    data?.status.scheduler,
    data?.status.collection,
    data?.status.discovery,
  ] as (RunState | undefined)[];
  const failure = failures.find((state) => state?.failed);
  return (
    <div className="app-shell">
      <a className="skip-link" href="#rankings">
        跳转到项目榜单
      </a>
      <header className="masthead" id="top" ref={masthead}>
        <div className="frame masthead-inner">
          <a className="brand" href="/" aria-label="开源观察首页">
            <Icon name="mark" size={28} />
            <strong>开源观察</strong>
          </a>
          <section className="header-stats" aria-label="当前来源观测概览">
            <div>
              <span className="metric-label-full">正在追踪</span>
              <span className="metric-label-short" aria-hidden="true">
                追踪
              </span>
              <strong>
                {data
                  ? number(data.summary.tracked)
                  : resource.loading
                    ? "…"
                    : "—"}
              </strong>
            </div>
            <div title="当前来源追踪项目已知的 24 小时窗口净增量合计，含部分观测与负值；各项目实际区间可能不同。">
              <span className="metric-label-full">24 小时 Star 净增量</span>
              <span className="metric-label-short" aria-hidden="true">
                24h 净增量
              </span>
              <strong>
                {data
                  ? signed(data.summary.net24 ?? null)
                  : resource.loading
                    ? "…"
                    : "—"}
              </strong>
            </div>
            <div>
              <span className="metric-label-full">覆盖编程语言</span>
              <span className="metric-label-short" aria-hidden="true">
                语言
              </span>
              <strong>
                {data
                  ? number(data.languages.length)
                  : resource.loading
                    ? "…"
                    : "—"}
              </strong>
            </div>
          </section>
          <div className="masthead-end">
            <button
              type="button"
              className="icon-button"
              aria-label={dark ? "切换浅色主题" : "切换深色主题"}
              onClick={() => setDark((value) => !value)}
            >
              <Icon name={dark ? "sun" : "moon"} size={20} />
            </button>
            <a
              className="icon-button source-link"
              href="https://github.com/lizhian/github-trending-tracking"
              target="_blank"
              rel="noreferrer"
              aria-label="查看本站 GitHub 仓库"
            >
              <Icon name="github" size={20} />
            </a>
          </div>
        </div>
      </header>
      <main className="frame app-main">
        <h1 className="visually-hidden">开源趋势追踪</h1>
        {failure && (
          <p className="notice" role="status">
            更新异常：{failure.error || "采集尚未恢复"}
            。继续展示上次有效观测，后台将重试。
          </p>
        )}
        {data &&
          data.summary.fresh < data.summary.tracked && (
            <p className="notice" role="status">
              {data.summary.tracked - data.summary.fresh} 个项目未按计划完成更新，已超过 10
              分钟宽限并标记为数据过期。
            </p>
          )}
        <section
          className="workspace"
          id="rankings"
          aria-label="趋势探索工作台"
        >
          <div className="listing">
            <div className="list-controls" ref={controls}>
              <div className="view-tabs" ref={viewTabs} aria-label="趋势来源">
                <span
                  className="view-indicator"
                  aria-hidden="true"
                  style={{
                    transform: `translateX(${indicator.x}px) scaleX(${indicator.width})`,
                  }}
                />
                {(
                  [
                    ["github", "github趋势"],
                    ["linuxdo", "Linux.do开源趋势"],
                  ] as const
                ).map(([tab, label]) => (
                  <button
                    type="button"
                    key={tab}
                    aria-pressed={query.tab === tab}
                    onClick={() => {
                      setDraft("");
                      commit({ tab, q: "", language: "", topic: "", page: 1, project: "" });
                    }}
                  >
                    {label}
                    {query.tab === tab && (
                      <span>{data?.summary.tracked ?? "…"}</span>
                    )}
                  </button>
                ))}
              </div>
              <>
                  <div className="filters">
                    <label className="search-field">
                      <Icon name="search" />
                      <span className="visually-hidden">
                        搜索项目名称或简介
                      </span>
                      <input
                        ref={searchInput}
                        type="search"
                        name="q"
                        autoComplete="off"
                        value={draft}
                        onChange={(event) =>
                          setDraft(event.target.value.slice(0, 200))
                        }
                        placeholder="搜索项目、描述…"
                      />
                      <kbd>/</kbd>
                    </label>
                    <SearchSelect
                      label="编程语言"
                      all="全部语言"
                      name="language"
                      value={query.language}
                      options={options?.languages ?? []}
                      onChange={(language) => commit({ language, page: 1 })}
                    />
                    <SearchSelect
                      label="仓库 Topics"
                      all="全部 Topics"
                      name="topic"
                      value={query.topic}
                      options={options?.topics ?? []}
                      onChange={(topic) => commit({ topic, page: 1 })}
                    />
                    <button
                      type="button"
                      className="icon-button reset"
                      title="清除筛选"
                      aria-label="清除全部筛选"
                      onClick={clear}
                    >
                      <Icon name="reset" />
                    </button>
                    <button
                      type="button"
                      className="icon-button reset"
                      disabled={resource.loading}
                      aria-label="刷新数据"
                      title="只查询已采集结果"
                      onClick={() => setRefresh((value) => value + 1)}
                    >
                      <Icon name="refresh" />
                    </button>
                  </div>
                  <div
                    className="heading-scroll"
                    ref={headerScroll}
                    onScroll={(event) =>
                      syncHorizontal(event.currentTarget, tableScroll.current)
                    }
                  >
                    <table aria-label="项目排序">
                      <ProjectColumns />
                      <thead>
                        <tr>
                          <th scope="col" className="rank-head">
                            排名
                          </th>
                          {sortHeading("name", "项目名称", "name-col")}
                          <th scope="col" className="lang-col">
                            语言
                          </th>
                          {sortHeading("stars", "Star 总数")}
                          {WINDOWS.map((hours) => (
                            <React.Fragment key={hours}>
                              {sortHeading(String(hours), labels[hours])}
                            </React.Fragment>
                          ))}
                        </tr>
                      </thead>
                    </table>
                  </div>
              </>
            </div>
            <>
                {resource.error && (
                  <p className="notice error" role="alert">
                    {resource.error}{" "}
                    <button
                      type="button"
                      onClick={() => setRefresh((value) => value + 1)}
                    >
                      重试列表查询
                    </button>
                  </p>
                )}
                <div className="visually-hidden" role="status">
                  {resource.loading
                    ? "正在读取趋势数据…"
                    : data
                      ? `共 ${data.total} 个项目，第 ${data.page} / ${data.pages} 页`
                      : ""}
                </div>
                <div
                  className="table-wrap"
                  ref={tableScroll}
                  onScroll={(event) =>
                    syncHorizontal(event.currentTarget, headerScroll.current)
                  }
                  aria-busy={resource.loading}
                >
                  <table
                    id="project-data"
                    key={resource.value ? listPath : "loading"}
                    className={
                      resource.value && !resource.loading
                        ? "list-arrival"
                        : undefined
                    }
                  >
                    <ProjectColumns />
                    <caption className="visually-hidden">
                      追踪项目及其 Star 净增量
                    </caption>
                    <thead>
                      <tr>
                        <th className="visually-hidden" scope="col">
                          排名
                        </th>
                        <th className="visually-hidden" scope="col">
                          项目名称
                        </th>
                        <th className="visually-hidden" scope="col">
                          语言
                        </th>
                        <th className="visually-hidden" scope="col">
                          Star 总数
                        </th>
                        {WINDOWS.map((hours) => (
                          <th
                            className="visually-hidden"
                            scope="col"
                            key={hours}
                          >
                            {labels[hours]}
                          </th>
                        ))}
                      </tr>
                    </thead>
                    <tbody>
                      {data?.items.length ? (
                        data.items.map((project, index) => {
                          const [owner, ...name] = project.name.split("/");
                          return (
                            <tr
                              key={project.id}
                              onClick={() => selectProject(project)}
                              className={
                                selected?.id === project.id ? "selected" : ""
                              }
                            >
                              <td className="rank">
                                {String(
                                  (data.page - 1) * data.pageSize + index + 1,
                                ).padStart(2, "0")}
                              </td>
                              <td className="repo-cell">
                                <button
                                  type="button"
                                  className="repo-button"
                                  aria-pressed={selected?.id === project.id}
                                  aria-label={`查看 ${project.name} 趋势`}
                                  title={project.name}
                                >
                                  <span className="owner">{owner}</span>
                                  <span className="slash">/</span>
                                  {name.join("/")}
                                </button>
                                <p
                                  className="repo-description"
                                  title={project.description ?? undefined}
                                >
                                  {project.stale ? "数据过期 / " : ""}
                                  {project.error ? "更新异常 / " : ""}
                                  {project.description || "仓库尚未提供简介。"}
                                </p>
                              </td>
                              <td className="lang-col">
                                <span className="lang">
                                  {project.language || "未提供"}
                                </span>
                              </td>
                              <td className="numeric">
                                <span
                                  title={number(project.stars)}
                                  aria-label={`${number(project.stars)} Star`}
                                >
                                  {compact(project.stars)}
                                </span>
                              </td>
                              {WINDOWS.map((hours) => (
                                <td className="numeric" key={hours}>
                                  <TrendValue trend={project.trends[hours]} />
                                </td>
                              ))}
                            </tr>
                          );
                        })
                      ) : (
                        <tr>
                          <td colSpan={8} className="empty-cell">
                            <strong>
                              {resource.loading
                                ? "正在连接观测数据"
                                : resource.error
                                  ? "暂时无法读取数据"
                                  : data?.summary.tracked
                                    ? "没有匹配的项目"
                                    : "暂无追踪项目"}
                            </strong>
                            <p>
                              {resource.loading
                                ? "请稍候…"
                                : resource.error
                                  ? "请重试列表查询。"
                                  : data?.summary.tracked
                                    ? "试试其他关键词、语言或 Topic。"
                                    : "后台采集后，项目会自动出现在这里。"}
                            </p>
                            {!resource.loading &&
                              !resource.error &&
                              !!data?.summary.tracked && (
                                <button type="button" onClick={clear}>
                                  清除全部筛选
                                </button>
                              )}
                          </td>
                        </tr>
                      )}
                    </tbody>
                  </table>
                </div>
                <div className="list-footer">
                  <span>
                    {data
                      ? `每页 ${data.pageSize} 项，共 ${number(data.total)} 项`
                      : "正在读取列表"}
                  </span>
                  <nav className="pagination" aria-label="项目列表分页">
                    <button
                      type="button"
                      className="icon-button"
                      aria-label="上一页"
                      disabled={!data || resource.loading || data.page <= 1}
                      onClick={() => commit({ page: (data?.page ?? 1) - 1 })}
                    >
                      <Icon name="previous" />
                    </button>
                    <span>{data ? `${data.page} / ${data.pages}` : "…"}</span>
                    <button
                      type="button"
                      className="icon-button"
                      aria-label="下一页"
                      disabled={
                        !data || resource.loading || data.page >= data.pages
                      }
                      onClick={() => commit({ page: (data?.page ?? 1) + 1 })}
                    >
                      <Icon name="next" />
                    </button>
                  </nav>
                </div>
            </>
          </div>
          <aside
              className="inspector"
              ref={inspector}
              aria-label="选中项目详情"
            >
              {selected && data ? (
                <Details
                  key={selected.id}
                  project={selected}
                  window={query.window}
                  refresh={refresh}
                  asOf={data.asOf}
                  onWindow={(window) => commit({ window })}
                  onTopic={(topic) => commit({ topic, page: 1 })}
                  onRefresh={() => setRefresh((value) => value + 1)}
                />
              ) : (
                <p className="detail-empty">
                  {resource.loading
                    ? "正在读取追踪项目…"
                    : "从榜单中选择一个项目，查看它的 Star 变化。"}
                </p>
              )}
              <details className="method" id="method">
                <summary>
                  数据说明<span aria-hidden="true">＋</span>
                </summary>
                <p>
                  {query.tab === "linuxdo"
                    ? "项目来自 Linux.do 开源推广 RSS 正文中的 GitHub 仓库链接。"
                    : "项目来自 GitHub Trending、HelloGithub 和 OpenGithubs。"}
                  GitHub 日榜、Linux.do RSS 与 Star 采样均在北京时间 01、05、09、11、13、15、17、19、21 点开始，分批完成；其余来源每天 09:00 开始抓取。新收录项目复用同仓库本轮的真实观测，尚无本轮观测时立即采样。所有访客共享观测，无需登录。
                </p>
                <p>
                  趋势窗口为滚动 24、72、168、720
                  小时，以项目最新成功观测为终点，仅保留最近 30
                  天内的实际快照，完整窗口的端点容差按应采时段及 10 分钟宽限判断。历史不足显示部分观测，不足两次快照显示积累中。负净增量原样展示，缺测区间断线，不补零或推算。
                </p>
                <p>
                  顶部净增量为当前来源已知项目窗口的合计，含部分观测，各项目实际区间可能不同；积累中项目不参与合计。语言数与项目数量也仅统计当前来源。
                </p>
                <p>
                  错过应采时段并超过 10 分钟宽限标为数据过期，正常的 2/4 小时间隔不算缺测。旧历史保留原有缺测含义。每日北京时间 04:00
                  按保留评分分别清理，每个来源目标保留 500
                  项；保护期、观测数据过期或观测不足的项目暂缓淘汰，因此可能超额。
                </p>
                <p>
                  最近成功采集：{time(collection?.succeededAt)}。最近采集尝试：
                  {time(collection?.attemptedAt)}。
                </p>
              </details>
          </aside>
        </section>
      </main>
    </div>
  );
}
createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
