import React, {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { createRoot } from "react-dom/client";
import {
  WINDOWS,
  type HistoryResult,
  type ListResult,
  type Project,
  type RunState,
  type Trend,
  type TrendSummary,
  type WindowHours,
} from "../shared/contracts";
import { SubmissionDialog } from "./SubmissionDialog";
import { VisitStats } from "./VisitStats";
import { SearchSelect } from "./SearchSelect";
import { projectProblems, ProjectProblemDetails, ProjectProblemIndicator } from "./ProjectProblems";
import { AITags } from "./AITags";
import { LanguageIcon } from "./LanguageIcon";
import { projectHeat } from "./heat";
import "./style.css";
import "./apple.css";

const labels: Record<WindowHours, string> = {
  24: "1天",
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
const newAdmissionLabel = (enteredAt: number, now: number) => {
  const elapsed = now - enteredAt;
  if (!Number.isFinite(enteredAt) || elapsed < 0 || elapsed >= 24 * 3_600_000)
    return null;
  const hours = Math.floor(elapsed / 3_600_000);
  return hours === 0 ? "🌟 just now" : `🌟 ${hours}h ago`;
};
const stateLabel = (trend: TrendSummary) =>
  trend.basis === "unknown"
    ? "暂不可知"
    : trend.basis === "carried"
      ? "含持平计算"
      : trend.state === "accumulating"
        ? "积累中"
        : trend.state === "partial"
          ? "部分观测"
          : "完整观测";
const apiBase = (import.meta.env.VITE_API_URL ?? "").replace(/\/$/, "");
const sorts = ["name", "heat", "stars", ...WINDOWS.map(String)];
const defaultSort = "24";
const languageQueryPrefix = "language:";
const admissionFilters = [
  { value: "24", label: "<1天", days: 1, atLeast: false },
  { value: "gte24", label: ">1天", days: 1, atLeast: true },
  { value: "72", label: "<3天", days: 3, atLeast: false },
  { value: "gte72", label: ">3天", days: 3, atLeast: true },
  { value: "168", label: "<7天内", days: 7, atLeast: false },
  { value: "gte168", label: ">7天", days: 7, atLeast: true },
] as const;
type AdmissionFilter = (typeof admissionFilters)[number];
const admissionOptions = admissionFilters.map(({ value }) => value);
const admissionFilterFor = (value: string) =>
  admissionFilters.find((filter) => filter.value === value);

function matchesAdmissionFilter(enteredAt: number, now: number, filter?: AdmissionFilter) {
  if (!filter) return true;
  const elapsed = now - enteredAt;
  const threshold = filter.days * 86_400_000;
  return elapsed >= 0 && (filter.atLeast ? elapsed >= threshold : elapsed < threshold);
}

function isDialogBackdrop(event: React.MouseEvent<HTMLDialogElement>) {
  if (event.target !== event.currentTarget) return false;
  const { left, top, right, bottom } = event.currentTarget.getBoundingClientRect();
  return event.clientX < left || event.clientX > right || event.clientY < top || event.clientY > bottom;
}

interface QueryState {
  q: string;
  admitted: "" | AdmissionFilter["value"];
  tab: "github" | "linuxdo";
  sort: string;
  direction: "asc" | "desc";
  project: string;
  window: WindowHours;
}
function readLocation(): QueryState {
  const params = new URLSearchParams(location.search);
  const text = (key: string) => (params.get(key) ?? "").slice(0, 200);
  const hours = Number(text("window")) as WindowHours;
  return {
    q: [text("q"), text("language"), text("topic")].filter(Boolean).join(" "),
    admitted: admissionFilterFor(text("admitted"))?.value ?? "",
    tab: text("tab") === "linuxdo" ? "linuxdo" : "github",
    sort: sorts.includes(text("sort")) ? text("sort") : defaultSort,
    direction: text("direction") === "asc" ? "asc" : "desc",
    project: text("project"),
    window: WINDOWS.includes(hours) ? hours : 720,
  };
}
function locationParameters(query: QueryState) {
  return new URLSearchParams({
    q: query.q,
    admitted: query.admitted,
    sort: query.sort,
    direction: query.direction,
  });
}
function locationFor(query: QueryState) {
  const params = locationParameters(query);
  for (const [key, value] of [...params]) {
    if (
      !value ||
      (key === "sort" && value === defaultSort) ||
      (key === "direction" && value === "desc")
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
    | "reset"
    | "github"
    | "external"
    | "previous"
    | "next"
    | "bell";
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
    bell: (
      <>
        <path d="M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9M10 21h4" />
      </>
    ),
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
const chartDate = new Intl.DateTimeFormat("zh-CN", {
  timeZone: "Asia/Shanghai",
  year: "numeric",
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  hourCycle: "h23",
});

function chartIntervalTime(from: number, to: number) {
  const parts = Object.fromEntries(
    chartDate.formatToParts(to).map(({ type, value }) => [type, value]),
  );
  const date = `${parts.year}-${parts.month}-${parts.day}`;
  const hour = 3_600_000;
  const hours = Math.floor(to / hour) - Math.floor(from / hour);
  if (hours > 0 && hours % 24 === 0 && parts.hour === "00") {
    return { date, duration: `${hours / 24}d` };
  }
  const minutes = Math.floor((to - from) / 60_000);
  const duration = hours > 0 ? `${hours}h` : minutes === 0 ? "<1m" : `${minutes}m`;
  return { date: `${date}日 ${parts.hour}时`, duration };
}

function Chart({ trend }: { trend: Trend }) {
  const [activeIndex, setActiveIndex] = useState<number | null>(null);
  const points = trend.points;
  const intervals = points.slice(1).map(([to, stars], index) => {
    const [from, previousStars] = points[index];
    return {
      from,
      to,
      delta: stars - previousStars,
      missing: trend.gaps.some(([start, end]) => from === start && to === end),
      ...chartIntervalTime(from, to),
    };
  });
  const values = intervals.map(({ delta }) => delta);
  const smallest = Math.min(0, ...values);
  const largest = Math.max(0, ...values);
  const mixed = smallest < 0 && largest > 0;
  const magnitude = Math.max(-smallest, largest);
  const min = mixed ? -magnitude : smallest;
  const max = mixed ? magnitude : largest;
  const ticks = [...new Set([max, Math.round((max + min) / 2), min])];
  const tickLabels = ticks.map((value) => (value === 0 ? "0" : signed(value)));
  const viewWidth = Math.max(
    288,
    248 + Math.max(...tickLabels.map((label) => label.length)) * 7,
  );
  const first = points[0][0];
  const last = points[points.length - 1][0];
  const span = last - first || 1;
  const x = (at: number) => 4 + ((at - first) / span) * 228;
  const y = (value: number) =>
    min === max ? 67.5 : 106 - ((value - min) / (max - min)) * 77;
  const zero = y(0);
  const active = activeIndex === null ? undefined : intervals[activeIndex];
  return (
    <div className="chart-content">
      {active && (
        <div className="chart-detail">
          <span>{active.date}</span>
          <strong>{active.duration} {active.delta === 0 ? "0" : signed(active.delta)}</strong>
          {active.missing && <span className="chart-detail-note">缺测</span>}
        </div>
      )}
      <svg
        className="chart"
        viewBox={`0 0 ${viewWidth} 145`}
        role="group"
        aria-label={`相邻快照 Star 净增量柱状图，${stateLabel(trend)}，窗口净增量 ${trend.basis === "unknown" ? "暂不可知" : signed(trend.delta)}，${trend.gaps.length} 段缺测`}
      >
        {ticks.map((value, index) => (
          <g key={value}>
            <line
              className={value === 0 ? "chart-zero" : "chart-grid"}
              x1="4" x2="232" y1={y(value)} y2={y(value)}
            />
            <text x="240" y={y(value) + 3}>{tickLabels[index]}</text>
          </g>
        ))}
        {intervals.map(({ from, to, delta, missing, date, duration }, index) => {
          if (from === to) return null;
          const width = Math.min(28, (x(to) - x(from)) * 0.76);
          return (
            <rect
              key={index}
              className={`chart-bar${delta < 0 ? " negative" : ""}${missing ? " missing" : ""}`}
              x={(x(from) + x(to) - width) / 2}
              y={delta === 0 ? zero - 0.5 : Math.min(zero, y(delta))}
              width={width}
              height={delta === 0 ? 1 : Math.abs(y(delta) - zero)}
              role="img"
              tabIndex={0}
              aria-label={`${date}，${duration}，${delta === 0 ? "0" : signed(delta)} Star${missing ? "，缺测区间" : ""}`}
              onPointerEnter={() => setActiveIndex(index)}
              onPointerLeave={() => setActiveIndex(null)}
              onFocus={() => setActiveIndex(index)}
              onBlur={() => setActiveIndex(null)}
            />
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
    </div>
  );
}
function ProjectColumns() {
  return (
    <colgroup>
      <col className="rank-column" />
      <col className="name-column" />
      {Array.from({ length: 6 }, (_, index) => (
        <col className="number-column" key={index} />
      ))}
    </colgroup>
  );
}
const heatNumber = new Intl.NumberFormat("zh-CN", {
  minimumFractionDigits: 1,
  maximumFractionDigits: 1,
});
function HeatValue({ project }: { project: Project }) {
  const heat = projectHeat(project);
  const unknown = WINDOWS.some((hours) => project.trends[hours].basis === "unknown");
  return (
    <span
      className={heat === null ? "accumulating" : undefined}
      title={heat === null
        ? unknown ? "观测数据过期，暂无法计算热度" : "至少实际观测 1天且四窗口均可计算后展示热度"
        : "四窗口正向日均 Star 增长，按 60%、25%、10%、5% 加权"}
    >
      {heat === null ? unknown ? "—" : "积累中" : heat > 1000 ? `${(heat / 1000).toFixed(1)}k` : heatNumber.format(heat)}
    </span>
  );
}
function TrendValue({ trend, showState = false }: { trend: TrendSummary; showState?: boolean }) {
  const hint = `${stateLabel(trend)}${trend.start && trend.end ? `，${time(trend.start)} 至 ${time(trend.end)}` : ""}${trend.gaps.length ? `，${trend.gaps.length} 段缺测` : ""}`;
  return (
    <>
      <span
        className={
          trend.delta === null
            ? "accumulating"
            : `gain ${trend.delta < 0 ? "negative" : ""}`
        }
        title={`${trend.basis === "unknown" ? "暂不可知" : signed(trend.delta)}，${hint}`}
        aria-label={`${trend.basis === "unknown" ? "暂不可知" : signed(trend.delta)}，${hint}`}
      >
        {trend.basis === "unknown" ? "暂不可知" : compactSigned(trend.delta)}
      </span>
      {showState && trend.delta !== null && trend.basis !== "unknown" &&
        (trend.basis === "carried" || trend.state === "partial") && (
          <span className="partial" aria-hidden="true">{stateLabel(trend)}</span>
        )}
    </>
  );
}

function StarValue({ stars }: { stars: number }) {
  return (
    <span title={number(stars)} aria-label={`${number(stars)} Star`}>
      {compact(stars)}
    </span>
  );
}

function ProjectButton({
  project, now, selected, onClick,
}: {
  project: Project;
  now: number;
  selected: boolean;
  onClick?: () => void;
}) {
  const [owner, ...name] = project.name.split("/");
  const admissionLabel = newAdmissionLabel(project.enteredAt, now);
  return (
    <button
      type="button"
      className="repo-button"
      aria-pressed={onClick ? undefined : selected}
      aria-haspopup={onClick ? "dialog" : undefined}
      aria-label={`查看 ${project.name} 趋势${admissionLabel ? `，新入库 ${admissionLabel}` : ""}`}
      title={project.name}
      onClick={onClick}
    >
      <span className="repo-name">
        <span className="owner">{owner}</span>
        <span className="slash">/</span>
        {name.join("/")}
      </span>
      {admissionLabel && (
        <span className="new-admission" title={`新入库：${time(project.enteredAt)}`}>
          {admissionLabel}
        </span>
      )}
    </button>
  );
}
function homepageURL(value: string | null): URL | null {
  if (!value?.trim()) return null;
  const address = value.trim();
  try {
    const url = new URL(
      address.startsWith("//")
        ? `https:${address}`
        : /^[a-z][a-z\d+.-]*:/i.test(address)
          ? address
          : `https://${address}`,
    );
    return url.protocol === "https:" || url.protocol === "http:" ? url : null;
  } catch {
    return null;
  }
}

function Details({
  project,
  window,
  refresh,
  asOf,
  onWindow,
  onTopic,
  onRefresh,
  onBack,
}: {
  project: Project;
  window: WindowHours;
  refresh: number;
  asOf: number;
  onWindow: (hours: WindowHours) => void;
  onTopic: (topic: string) => void;
  onRefresh: () => void;
  onBack?: () => void;
}) {
  const [retry, setRetry] = useState(0);
  const history = useApi<HistoryResult>(
    `/api/projects/${encodeURIComponent(project.id)}/history?window=${window}`,
    `${project.observedAt}:${refresh}:${retry}`,
  );
  const current = history.value;
  const trend = current?.trend;
  const [owner, ...name] = project.name.split("/");
  const homepage = homepageURL(project.homepage);
  const description = project.description?.trim();
  const summary = project.aiSummary?.trim();
  const aiTags = project.aiTags ?? [];
  const protectedNow = asOf - project.enteredAt < 24 * 3_600_000;
  const realPoints = current?.points;
  const days = realPoints?.length
    ? (realPoints[realPoints.length - 1][0] - realPoints[0][0]) / 86_400_000
    : null;
  return (
    <section className="detail-content" aria-label={`${project.name} 详情`}>
      <div className="detail-title-row">
        {onBack && (
          <button type="button" className="mobile-details-back" aria-label="返回项目榜单" onClick={onBack}>
            <Icon name="previous" size={24} />
          </button>
        )}
      <h2 className="detail-title">
        <a
          href={`https://github.com/${project.name}`}
          target="_blank"
          rel="noreferrer"
        >
          <span className="detail-owner">{owner} / </span>
          <wbr />
          <span className="detail-repo-name">
            {name.join("/")}
            <span className="detail-title-external"><Icon name="external" size={12} /></span>
          </span>
        </a>
      </h2>
      </div>
      {(description || homepage || summary || aiTags.length > 0) && (
        <div className="detail-info">
          {description && <p className="detail-description">{description}</p>}
          {homepage && (
            <a
              className="detail-homepage"
              href={homepage.href}
              target="_blank"
              rel="noopener noreferrer"
              aria-label={`项目网站：${homepage.href}`}
            >
              <svg
                viewBox="0 0 24 24"
                width="18"
                height="18"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.8"
                strokeLinecap="round"
                strokeLinejoin="round"
                aria-hidden="true"
              >
                <path d="M10 13a5 5 0 0 0 7 .1l3-3a5 5 0 0 0-7.1-7.1l-1.7 1.7" />
                <path d="M14 11a5 5 0 0 0-7-.1l-3 3a5 5 0 0 0 7.1 7.1l1.7-1.7" />
              </svg>
              <span>{homepage.href.replace(/^https?:\/\//, "")}</span>
            </a>
          )}
          {summary && <p className="detail-description" aria-label="AI 项目总结">{summary}</p>}
          {aiTags.length > 0 && (
            <div className="detail-ai-tags" aria-label="AI 项目标签">
              {aiTags.map(tag => <button type="button" className="ai-tag" key={tag} onClick={() => onTopic(tag)} aria-label={`搜索 AI 标签 ${tag}`}>{tag}</button>)}
            </div>
          )}
        </div>
      )}
      {project.topics.length > 0 && (
        <div className="topics" aria-label="仓库 Topics">
          {project.topics.map((topic) => (
            <button
              type="button"
              className="topic-button"
              key={topic}
              onClick={() => onTopic(topic)}
              aria-label={`筛选 Topic ${topic}`}
            >
              {topic}
            </button>
          ))}
        </div>
      )}
      {!!(current?.unavailableAt ?? project.unavailableAt) && (
        <p className="notice" role="status">仓库不可访问，按最后已知 Star 总数计算。</p>
      )}
      {((current?.stale ?? project.stale) || project.error) && (
        <p className="notice" role="status">
          {(current?.stale ?? project.stale) ? "数据过期，未在应采时段及 10 分钟宽限内成功更新。" : ""}
          {project.error ? "项目更新异常，继续显示有效观测。" : ""}
        </p>
      )}
      <div className="detail-stat">
        <span>Star 总数</span>
        <div className="detail-star-line">
          <strong>{number(current?.points.at(-1)?.[1] ?? project.stars)}</strong>
          {trend && !history.loading && (
            <span
              className={`delta-badge ${trend.delta !== null && trend.delta < 0 ? "negative" : ""}`}
            >
              {trend.basis === "unknown" ? "暂不可知" : signed(trend.delta)}
            </span>
          )}
        </div>
      </div>
      <div className="history-region" aria-busy={history.loading}>
        <div className="chart-heading">
          <span>Star 净增量</span>
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
            trend.points.length >= 2 ? (
              <Chart trend={trend} />
            ) : (
              <p className="chart-message">
                {trend.points.length === 1
                  ? "至少需要两次快照才能显示区间差值"
                  : trend.basis === "carried"
                    ? "此窗口无真实观测，增量按持平计算"
                    : trend.basis === "unknown" ? "此窗口暂无有效观测" : "积累中"}
              </p>
            )
          ) : null}
        </div>
        <div className="chart-feedback">
          {!history.loading &&
            !history.error &&
            current &&
            (current.gaps.length > 0 || current.stale) && (
              <p className="chart-note" role="status">
                {current.gaps.length ? `浅色柱表示缺测区间（${current.gaps.length} 段）` : ""}
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
              ? trend.basis === "unknown"
                ? "暂不可知"
                : signed(trend.delta)
              : "暂不可用"}
        </dd>
        <dt>保护期</dt>
        <dd>{protectedNow ? "保护中" : "已结束"}</dd>
      </dl>
      <section className="record-times" aria-labelledby="observation-title">
        <h3 id="observation-title">观测详情</h3>
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
          <dt>最后成功观测</dt>
          <dd>{time(current?.observedAt ?? project.observedAt)}</dd>
        </dl>
      </section>
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
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 60_000);
    return () => window.clearInterval(timer);
  }, []);
  const [draft, setDraft] = useState(query.q);
  const [refresh, setRefresh] = useState(0);
  const [submissionOpen, setSubmissionOpen] = useState(false);
  const messagesDialog = useRef<HTMLDialogElement>(null);
  const messageBackdropPress = useRef(false);
  const mobileDetails = useRef<HTMLDialogElement>(null);
  const [isMobile, setIsMobile] = useState(() => matchMedia("(max-width:760px)").matches);
  const [mobileDetailsOpen, setMobileDetailsOpen] = useState(false);
  useEffect(() => {
    const media = matchMedia("(max-width:760px)");
    const update = () => {
      setIsMobile(media.matches);
      if (!media.matches) setMobileDetailsOpen(false);
    };
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);
  useEffect(() => {
    const dialog = mobileDetails.current;
    if (!isMobile || !mobileDetailsOpen || !dialog) return;
    dialog.showModal();
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "clip";
    return () => {
      dialog.close();
      document.body.style.overflow = previousOverflow;
    };
  }, [isMobile, mobileDetailsOpen]);
  const pushNext = useRef(false);
  const listing = useRef<HTMLDivElement>(null);
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
  const searchTag = (tag: string) => {
    setMobileDetailsOpen(false);
    setDraft(tag);
    commit({ q: tag });
  };
  const searchLanguage = (language: string) => {
    const q = languageQueryPrefix + language;
    setDraft(q);
    commit({ q });
  };
  const listPath = `/api/projects?type=${query.tab === "linuxdo" ? "linux.do" : "github"}`;
  const previousListPath = useRef(listPath);
  const resource = useApi<ListResult>(listPath, refresh);
  const data = resource.value;
  const items = useMemo(() => {
    const search = query.q.trim().toLowerCase();
    const language = search.startsWith(languageQueryPrefix)
      ? search.slice(languageQueryPrefix.length).trim()
      : null;
    const keywords = search.split(/\s+/).filter(Boolean);
    const admissionFilter = admissionFilterFor(query.admitted);
    const compare = new Intl.Collator("en").compare;
    return (data?.items ?? []).filter((project) =>
      (language !== null
        ? !!language && project.language?.trim().toLowerCase() === language
        : keywords.every((keyword) =>
          `${project.name} ${project.description ?? ""} ${project.language ?? ""} ${project.topics.join(" ")} ${project.aiSummary ?? ""} ${(project.aiTags ?? []).join(" ")}`.toLowerCase().includes(keyword),
        )) && matchesAdmissionFilter(project.enteredAt, now, admissionFilter),
    ).sort((a, b) => {
      let order: number;
      if (query.sort === "name") order = compare(a.name.toLowerCase(), b.name.toLowerCase());
      else {
        const av = query.sort === "heat" ? projectHeat(a) : query.sort === "stars" ? a.stars : a.trends[Number(query.sort) as WindowHours].delta;
        const bv = query.sort === "heat" ? projectHeat(b) : query.sort === "stars" ? b.stars : b.trends[Number(query.sort) as WindowHours].delta;
        if (av === null || bv === null) {
          if (av !== bv) return av === null ? 1 : -1;
          return compare(a.name, b.name);
        }
        order = av - bv;
      }
      return (query.direction === "desc" ? -order : order) || compare(a.name, b.name);
    });
  }, [data, query.q, query.admitted, query.sort, query.direction, now]);
  // Filtering changes the list, not the open detail or its request lifecycle.
  const selected = data?.items.find((project) => project.id === query.project) ?? items[0] ?? data?.items[0];
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
      setMobileDetailsOpen(false);
    };
    addEventListener("popstate", restore);
    return () => removeEventListener("popstate", restore);
  }, []);
  useEffect(() => {
    if (draft === query.q) return;
    const timer = setTimeout(() => commit({ q: draft }), 300);
    return () => clearTimeout(timer);
  }, [draft, query.q, commit]);
  useEffect(() => {
    if (!data || resource.loading || resource.error)
      return;
    const project = selected?.id ?? "";
    if (query.project !== project)
      commit({ project }, false);
  }, [
    data,
    resource.loading,
    resource.error,
    selected?.id,
    query.project,
    query.tab,
    commit,
  ]);
  useLayoutEffect(() => {
    if (inspector.current) inspector.current.scrollTop = 0;
  }, [selected?.id]);
  useLayoutEffect(() => {
    if (previousListPath.current === listPath) return;
    previousListPath.current = listPath;
    if (tableScroll.current) tableScroll.current.scrollTop = 0;
    if (matchMedia("(max-width:760px)").matches)
      listing.current?.scrollIntoView({ block: "start", behavior: "instant" });
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
  }, [query.tab, isMobile]);
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
        !isMobile &&
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
  }, [isMobile]);
  const clear = () => {
    setDraft("");
    commit({ q: "", admitted: "" });
  };
  const changeSort = (sort: string) =>
    commit({
      sort,
      direction:
        query.sort === sort && query.direction === "desc" ? "asc" : "desc",
    });
  const selectProject = (project: Project) => {
    commit({ project: project.id });
    if (inspector.current) inspector.current.scrollTop = 0;
    if (isMobile) setMobileDetailsOpen(true);
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
  const emptyList = (
    <>
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
              ? "试试其他关键词、语言、Topic 或 AI 标签。"
              : "后台采集后，项目会自动出现在这里。"}
      </p>
      {!resource.loading && !resource.error && !!data?.summary.tracked && (
        <button type="button" onClick={clear}>清除全部筛选</button>
      )}
    </>
  );
  const failures = [
    data?.status.scheduler,
    data?.status.collection,
  ] as (RunState | undefined)[];
  const failure = failures.find((state) => state?.failed);
  const admission = data?.status.admission as RunState | undefined;
  const discovery = data?.status.discovery as RunState | undefined;
  const problemProjects = data?.items.filter((project) => projectProblems(project).length > 0) ?? [];
  const hasCollectionNotice =
    problemProjects.length > 0 || failure || discovery?.failed || admission?.failed ||
    (data && data.summary.fresh < data.summary.tracked);
  const inspectorContent = (
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
                  onTopic={searchTag}
                  onRefresh={() => setRefresh((value) => value + 1)}
                  onBack={isMobile ? () => setMobileDetailsOpen(false) : undefined}
                />
              ) : (
                <p className="detail-empty">
                  {resource.loading
                    ? "正在读取追踪项目…"
                    : "从榜单中选择一个项目，查看它的 Star 变化。"}
                </p>
              )}
          </aside>
  );
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
          <div className="list-controls masthead-controls">
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
                    ["github", "Github趋势"],
                    ["linuxdo", "Linux.do开源趋势"],
                  ] as const
                ).map(([tab, label]) => (
                  <button
                    type="button"
                    key={tab}
                    aria-pressed={query.tab === tab}
                    onClick={() => {
                      setDraft("");
                      commit({ tab, q: "", admitted: "", project: "" });
                    }}
                  >
                    {label}
                  </button>
                ))}
              </div>
              {!isMobile && (
                  <div className="filters">
                    <label className="search-field">
                      <Icon name="search" />
                      <span className="visually-hidden">
                        搜索项目、描述、语言、Topic、AI 总结或 AI 标签
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
                        placeholder="搜索项目、语言、AI 总结或标签…"
                      />
                    </label>
                    <SearchSelect
                      label="入库时长"
                      all="全部"
                      name="admitted"
                      value={query.admitted}
                      options={admissionOptions}
                      searchable={false}
                      optionLabel={(value) => admissionFilterFor(value)?.label ?? value}
                      onChange={(value) => commit({ admitted: admissionFilterFor(value)?.value ?? "" })}
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
                  </div>
              )}
          </div>
          <div className="masthead-end">
            <button type="button" className="submission-open" onClick={() => setSubmissionOpen(true)}>投稿</button>
            <button
              type="button"
              className="icon-button collection-messages"
              aria-label={hasCollectionNotice ? "采集消息，有异常提示" : "采集消息"}
              aria-haspopup="dialog"
              aria-controls="collection-messages"
              title="采集消息"
              onClick={() => messagesDialog.current?.showModal()}
            >
              <Icon name="bell" size={20} />
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
      {submissionOpen && <SubmissionDialog apiBase={apiBase} onClose={() => setSubmissionOpen(false)} />}
      <dialog
        id="collection-messages"
        className="collection-dialog"
        ref={messagesDialog}
        aria-labelledby="collection-messages-title"
        onPointerDown={(event) => { messageBackdropPress.current = isDialogBackdrop(event); }}
        onClick={(event) => {
          const close = messageBackdropPress.current && isDialogBackdrop(event);
          messageBackdropPress.current = false;
          if (close) event.currentTarget.close();
        }}
      >
        <div className="collection-heading">
          <h2 id="collection-messages-title">
            {query.tab === "linuxdo" ? "Linux.do" : "GitHub"} 采集消息
          </h2>
          <button type="button" className="icon-button" aria-label="关闭采集消息" onClick={() => messagesDialog.current?.close()}>×</button>
        </div>
        <div aria-live="polite">
          {failure && (
            <p>
              更新异常：{failure.error || "采集尚未恢复"}
              。继续展示上次有效观测，后台将重试。
            </p>
          )}
          {!!discovery?.failed && (
            <p>
              趋势源获取异常：{discovery.error || "暂时无法获取新候选"}。后台将重试。
            </p>
          )}
          {!!admission?.failed && (
            <p>
              部分候选暂未收录，后台将重试。
            </p>
          )}
          {data &&
            data.summary.fresh < data.summary.tracked && (
              <p>
                {data.summary.tracked - data.summary.fresh} 个项目未按计划完成更新，已超过 10
                分钟宽限并标记为数据过期。
              </p>
            )}
          {!!data?.sources?.length && (
            <section className="collection-sources" aria-labelledby="collection-sources-title">
              <h3 id="collection-sources-title">趋势源采集情况</h3>
              <p>解析结果可能包含已收录或重复项目；HelloGitHub 的解析结果为待解析的详情链接。</p>
              <ul className="collection-source-list">
                {data.sources.map((source) => (
                  <li key={source.id}>
                    <strong>{source.name}</strong>
                    <span className={source.failed ? "source-failed" : ""}>
                      {source.failed ? "异常，等待重试" : !source.attemptedAt ? "尚未执行" : source.languagesTotal && source.languagesCompleted !== source.languagesTotal ? "部分完成" : "成功"}
                    </span>
                    <dl>
                      <dt>最近尝试</dt><dd>{source.attemptedAt ? time(source.attemptedAt) : "尚未执行"}</dd>
                      <dt>最近成功</dt><dd>{time(source.succeededAt)}</dd>
                      <dt>解析结果数</dt><dd>{source.attemptedAt ? number(source.succeeded) : "—"}</dd>
                      {source.languagesTotal && <><dt>已完成语言</dt><dd>{source.languagesCompleted} / {source.languagesTotal}</dd></>}
                      <dt>{source.retryAfter ? "下次重试" : "下次执行"}</dt>
                      <dd>{!source.nextAt ? "等待调度" : source.nextAt <= data.asOf ? "已到期，等待调度" : time(source.nextAt)}</dd>
                    </dl>
                    {source.error && <p role="status">{source.error === "GitHub Search incomplete results" ? "搜索结果不完整，已保留返回的候选，将重试。" : source.error}</p>}
                  </li>
                ))}
              </ul>
            </section>
          )}
          {problemProjects.length > 0 && (
            <section className="collection-project-problems" aria-labelledby="project-problems-title">
              <h3 id="project-problems-title">有问题的项目（{problemProjects.length}）</h3>
              <ul className="collection-project-list" aria-label="有问题的项目">
                {problemProjects.map((project) => (
                  <li key={project.id}>
                    <strong>{project.name}</strong>
                    <ProjectProblemDetails project={project} />
                  </li>
                ))}
              </ul>
            </section>
          )}
          {!hasCollectionNotice && (
            <p>
              {resource.loading
                ? "正在读取采集状态…"
                : resource.error
                  ? "暂时无法获取采集状态。"
                  : "暂无采集异常提示。"}
            </p>
          )}
        </div>
      </dialog>
      <main className="frame app-main">
        <h1 className="visually-hidden">开源趋势追踪</h1>
        <section
          className="workspace"
          id="rankings"
          aria-label="趋势探索工作台"
        >
          <div className="listing" ref={listing}>
            {!isMobile && (
              <div className="list-controls">
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
                          {WINDOWS.map((hours) => (
                            <React.Fragment key={hours}>
                              {sortHeading(String(hours), labels[hours])}
                            </React.Fragment>
                          ))}
                          {sortHeading("stars", "总数", "numeric stars-col")}
                          {sortHeading("heat", "热度", "numeric heat-col")}
                        </tr>
                      </thead>
                    </table>
                  </div>
              </div>
            )}
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
                      ? `共 ${items.length} 个项目`
                      : ""}
                </div>
                {isMobile ? (
                  items.length ? (
                    <ol aria-label="追踪项目" role="list" aria-busy={resource.loading} className="mobile-projects">
                      {items.map((project, index) => (
                        <li key={project.id}>
                          <div className="mobile-project-title">
                            <span className="rank">{String(index + 1).padStart(2, "0")}</span>
                            <ProjectButton
                              project={project}
                              now={now}
                              selected={selected?.id === project.id}
                              onClick={() => selectProject(project)}
                            />
                            <LanguageIcon language={project.language} onSearch={searchLanguage} />
                            <ProjectProblemIndicator project={project} />
                          </div>
                          {query.sort !== "name" && (
                            <div className="mobile-project-metric">
                              <span className="visually-hidden">
                                {query.sort === "stars" ? "Star 总数" : query.sort === "heat" ? "热度"
                                  : `${labels[Number(query.sort) as WindowHours]} Star 净增量`}
                              </span>
                              {query.sort === "stars" ? <StarValue stars={project.stars} />
                                : query.sort === "heat" ? <HeatValue project={project} />
                                : <TrendValue trend={project.trends[Number(query.sort) as WindowHours]} showState />}
                            </div>
                          )}
                          <button
                            type="button"
                            className="mobile-project-summary"
                            aria-haspopup="dialog"
                            onClick={() => selectProject(project)}
                          >
                            {project.aiSummary || project.description || "暂无项目介绍"}
                          </button>
                        </li>
                      ))}
                    </ol>
                  ) : (
                    <div className="empty-cell mobile-projects-empty">{emptyList}</div>
                  )
                ) : (
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
                      追踪项目、热度及其 Star 净增量
                    </caption>
                    <thead>
                      <tr>
                        <th className="visually-hidden" scope="col">
                          排名
                        </th>
                        <th className="visually-hidden" scope="col">
                          项目名称
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
                        <th className="visually-hidden" scope="col">总数</th>
                        <th className="visually-hidden" scope="col">热度</th>
                      </tr>
                    </thead>
                    <tbody>
                      {items.length ? (
                        items.map((project, index) => {
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
                                  index + 1,
                                ).padStart(2, "0")}
                              </td>
                              <td className="repo-cell">
                                <div className="repo-title">
                                  <ProjectButton project={project} now={now} selected={selected?.id === project.id} />
                                  <LanguageIcon language={project.language} onSearch={searchLanguage} />
                                  <AITags tags={project.aiTags ?? []} onTag={searchTag} />
                                  <ProjectProblemIndicator project={project} />
                                </div>
                                <p
                                  className="repo-description"
                                  title={project.aiSummary ?? project.description ?? undefined}
                                >
                                  {project.aiSummary || project.description || "暂无项目介绍"}
                                </p>
                              </td>
                              {WINDOWS.map((hours) => (
                                <td className="numeric" key={hours}>
                                  <TrendValue trend={project.trends[hours]} />
                                </td>
                              ))}
                              <td className="numeric stars-col">
                                <StarValue stars={project.stars} />
                              </td>
                              <td className="numeric heat-col">
                                <HeatValue project={project} />
                              </td>
                            </tr>
                          );
                        })
                      ) : (
                        <tr>
                          <td colSpan={8} className="empty-cell">
                            {emptyList}
                          </td>
                        </tr>
                      )}
                    </tbody>
                  </table>
                </div>
                )}
                <div className="list-footer">
                  <span>
                    {data
                      ? `列表生成于 ${time(data.asOf)}`
                      : "正在读取列表"}
                  </span>
                  <VisitStats />
          <section className="footer-stats" aria-label="当前来源观测概览">
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
            <div title="当前来源追踪项目已知的 1天窗口净增量合计，含部分观测与负值；各项目实际区间可能不同。">
              <span className="metric-label-full">1天 Star 净增量</span>
              <span className="metric-label-short" aria-hidden="true">
                1天 净增量
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
                </div>
            </>
          </div>
          {isMobile ? (
            <dialog className="mobile-details-dialog" ref={mobileDetails} aria-label="项目详情" onCancel={() => setMobileDetailsOpen(false)} onClose={() => setMobileDetailsOpen(false)}>
              {mobileDetailsOpen && inspectorContent}
            </dialog>
          ) : inspectorContent}
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
