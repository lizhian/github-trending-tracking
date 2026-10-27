import React, { Fragment, useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  WINDOWS,
  type HistoryResult,
  type ListResult,
  type Point,
  type Project,
  type RunState,
  type Trend,
  type WindowHours,
} from "../worker/contracts";
import "./style.css";

const labels: Record<WindowHours, string> = {
  24: "24 小时",
  72: "3 天",
  168: "7 天",
  720: "30 天",
};
const number = (value: number) => new Intl.NumberFormat("zh-CN").format(value);
const signed = (value: number | null) =>
  value === null ? "—" : `${value >= 0 ? "+" : "−"}${number(Math.abs(value))}`;
const time = (value: number | null | undefined) =>
  value
    ? new Date(value).toLocaleString("zh-CN", { hour12: false })
    : "尚无成功记录";
const interval = (trend: Trend) =>
  trend.start && trend.end
    ? `${time(trend.start)} — ${time(trend.end)}`
    : "尚无有效观测";
const stateLabel = (trend: Trend) =>
  trend.state === "accumulating"
    ? "积累中"
    : trend.state === "partial"
      ? "部分观测"
      : "完整观测";
const apiBase = (import.meta.env.VITE_API_URL ?? "").replace(/\/$/, "");
async function get<T>(path: string, signal: AbortSignal): Promise<T> {
  const response = await fetch(`${apiBase}${path}`, { signal });
  if (!response.ok) {
    const body = (await response.json().catch(() => null)) as {
      error?: string;
    } | null;
    throw new Error(body?.error ?? "请求失败，请稍后重试");
  }
  return response.json();
}
function Icon({
  name,
  size = 18,
}: {
  name: "radar" | "search" | "refresh" | "sun" | "moon" | "info" | "chevron";
  size?: number;
}) {
  const paths = {
    radar: (
      <>
        <circle cx="12" cy="12" r="8" />
        <circle cx="12" cy="12" r="3" />
        <path d="m12 12 7-7" />
      </>
    ),
    search: (
      <>
        <circle cx="10.5" cy="10.5" r="6.5" />
        <path d="m16 16 4.5 4.5" />
      </>
    ),
    refresh: (
      <>
        <path d="M20 7v5h-5M4 17v-5h5" />
        <path d="M6 6a8 8 0 0 1 14 6M4 12a8 8 0 0 0 14 6" />
      </>
    ),
    sun: (
      <>
        <circle cx="12" cy="12" r="4" />
        <path d="M12 2v2m0 16v2M2 12h2m16 0h2M5 5l2 2m10 10 2 2M5 19l2-2M17 7l2-2" />
      </>
    ),
    moon: <path d="M20.5 13.5A9 9 0 0 1 10.5 3a9 9 0 1 0 10 10.5Z" />,
    info: (
      <>
        <circle cx="12" cy="12" r="9" />
        <path d="M12 11v6m0-10v.2" />
      </>
    ),
    chevron: <path d="m9 5 7 7-7 7" />,
  };
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {paths[name]}
    </svg>
  );
}
function Chart({ trend, mini = false }: { trend: Trend; mini?: boolean }) {
  const points = trend.points;
  const width = mini ? 100 : 620,
    height = mini ? 30 : 210,
    pad = mini ? 2 : 12;
  const min = Math.min(...points.map((p) => p[1])),
    max = Math.max(...points.map((p) => p[1]));
  const begin = points[0]?.[0] ?? 0,
    end = points.at(-1)?.[0] ?? 0;
  const xy = ([t, stars]: Point) => [
    pad + ((t - begin) / (end - begin || 1)) * (width - 2 * pad),
    height - pad - ((stars - min) / (max - min || 1)) * (height - 2 * pad),
  ];
  const paths: Point[][] = [];
  for (const point of points) {
    const previous = paths.at(-1)?.at(-1);
    if (
      !previous ||
      trend.gaps.some(([from, to]) => previous[0] <= from && point[0] >= to)
    )
      paths.push([point]);
    else paths.at(-1)!.push(point);
  }
  return (
    <svg
      className={mini ? "sparkline" : "chart"}
      viewBox={`0 0 ${width} ${height}`}
      preserveAspectRatio="none"
      role={mini ? undefined : "img"}
      aria-hidden={mini || undefined}
      aria-label={
        mini
          ? undefined
          : `Star 总数曲线，${stateLabel(trend)}，净增量 ${signed(trend.delta)}，${trend.gaps.length} 段缺测`
      }
    >
      {!mini &&
        [0.25, 0.5, 0.75].map((y) => (
          <line
            key={y}
            x1={pad}
            x2={width - pad}
            y1={height * y}
            y2={height * y}
            className="chart-grid"
          />
        ))}
      {paths.map((segment, i) => (
        <polyline
          key={i}
          points={segment.map((p) => xy(p).join(",")).join(" ")}
          fill="none"
          stroke="currentColor"
          strokeWidth={mini ? 2 : 2.5}
          vectorEffect="non-scaling-stroke"
        />
      ))}
      {points.map((point, i) => {
        const [cx, cy] = xy(point);
        return (
          <circle
            key={i}
            cx={cx}
            cy={cy}
            r={mini ? 1.3 : 2.4}
            fill="currentColor"
          >
            <title>
              {time(point[0])} · {number(point[1])} Star
            </title>
          </circle>
        );
      })}
    </svg>
  );
}
function TrendCell({ trend }: { trend: Trend }) {
  return (
    <div
      className={`trend ${trend.delta !== null && trend.delta < 0 ? "negative" : ""}`}
      title={interval(trend)}
    >
      <div>
        <strong>{signed(trend.delta)}</strong>
        <small>
          {stateLabel(trend)}
          {trend.gaps.length > 0 ? " · 缺测" : ""}
        </small>
      </div>
      <Chart trend={trend} mini />
      <span className="sr-only">{interval(trend)}</span>
    </div>
  );
}
function Details({ project }: { project: Project }) {
  const [window, setWindow] = useState<WindowHours>(24);
  const [history, setHistory] = useState<HistoryResult | null>(null);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError("");
    setHistory(null);
    get<HistoryResult>(
      `/api/projects/${encodeURIComponent(project.id)}/history?window=${window}`,
      controller.signal,
    )
      .then(setHistory)
      .catch((e) => {
        if (!controller.signal.aborted) setError(e.message);
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [project.id, project.observedAt, window, retry]);
  return (
    <section
      id={`details-${project.id}`}
      className="details"
      aria-label={`${project.name} 详情`}
    >
      <div className="description">
        <span className="eyebrow">项目简介</span>
        <h3>{project.name.split("/").at(-1)}</h3>
        <p>{project.description || "仓库尚未提供简介。"}</p>
        <a
          href={`https://github.com/${project.name}`}
          target="_blank"
          rel="noreferrer"
        >
          在 GitHub 查看 ↗
        </a>
        <dl>
          <dt>本次入库</dt>
          <dd>{time(project.enteredAt)}</dd>
          <dt>最近上榜</dt>
          <dd>{time(project.listedAt)}</dd>
          <dt>最后成功观测</dt>
          <dd>{time(project.observedAt)}</dd>
        </dl>
      </div>
      <div className="chart-panel">
        <div className="chart-heading">
          <h3>Star 总数</h3>
          <div className="tabs" aria-label="图表趋势窗口">
            {WINDOWS.map((hours) => (
              <button
                key={hours}
                aria-pressed={window === hours}
                onClick={() => setWindow(hours)}
              >
                {labels[hours]}
              </button>
            ))}
          </div>
        </div>
        {loading && (
          <p className="chart-message" role="status">
            正在读取观测历史…
          </p>
        )}
        {error && (
          <div className="chart-message" role="alert">
            {error} <button onClick={() => setRetry(retry + 1)}>重试</button>
          </div>
        )}
        {history && (
          <>
            <div className="chart-values">
              <strong>{signed(history.trend.delta)}</strong>
              <span>
                {stateLabel(history.trend)} · {history.points.length} 次实际观测
              </span>
            </div>
            {history.points.length ? (
              <Chart trend={history.trend} />
            ) : (
              <p className="chart-message">积累中，尚无保留期内的观测。</p>
            )}
            <div className="chart-axis">
              <span>{time(history.trend.start)}</span>
              <span>{time(history.trend.end)}</span>
            </div>
            <p className="chart-note">
              {interval(history.trend)}
              <br />
              {history.points.length > 0 &&
                `Star 总数 ${number(history.points[0][1])} → ${number(history.points.at(-1)![1])}。`}
              {history.gaps.length > 0
                ? ` ${history.gaps.length} 段采样缺口，曲线已断开。`
                : ""}
              {history.stale ? " 数据已超过 2 小时未成功更新。" : ""}
            </p>
          </>
        )}
      </div>
      <div className="topics">
        <span>仓库标签（Topics）</span>
        {project.topics.length ? (
          project.topics.map((topic) => (
            <span className="topic" key={topic}>
              {topic}
            </span>
          ))
        ) : (
          <span>仓库尚未提供 Topics</span>
        )}
      </div>
    </section>
  );
}
function App() {
  const [q, setQ] = useState(""),
    [search, setSearch] = useState("");
  const [language, setLanguage] = useState(""),
    [topic, setTopic] = useState("");
  const [sort, setSort] = useState("24"),
    [direction, setDirection] = useState("desc"),
    [page, setPage] = useState(1);
  const [data, setData] = useState<ListResult | null>(null),
    [loading, setLoading] = useState(true),
    [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0),
    [expanded, setExpanded] = useState<string | null>(null);
  const [dark, setDark] = useState(() => {
    try {
      return localStorage.getItem("theme") === "dark";
    } catch {
      return false;
    }
  });
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    document.documentElement.dataset.theme = dark ? "dark" : "light";
    try {
      localStorage.setItem("theme", dark ? "dark" : "light");
    } catch {
      /* Preference storage is optional. */
    }
  }, [dark]);
  useEffect(() => {
    const timer = setTimeout(() => {
      setSearch(q);
      setPage(1);
    }, 300);
    return () => clearTimeout(timer);
  }, [q]);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError("");
    const params = new URLSearchParams({
      q: search,
      language,
      topic,
      sort,
      direction,
      page: String(page),
    });
    get<ListResult>(`/api/projects?${params}`, controller.signal)
      .then((result) => {
        setData(result);
        setExpanded(null);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(e.message);
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [search, language, topic, sort, direction, page, refresh]);
  function changeSort(value: string) {
    setSort(value);
    setDirection(value === sort && direction === "desc" ? "asc" : "desc");
    setPage(1);
  }
  const clear = () => {
    setQ("");
    setSearch("");
    setLanguage("");
    setTopic("");
    setPage(1);
  };
  const tools = (
    <>
      <button onClick={() => dialog.current?.showModal()}>
        <Icon name="info" />
        数据说明
      </button>
      <button
        aria-label={dark ? "切换浅色主题" : "切换深色主题"}
        onClick={() => setDark(!dark)}
      >
        <Icon name={dark ? "sun" : "moon"} />
        <span>{dark ? "浅色模式" : "深色模式"}</span>
      </button>
    </>
  );
  const collection = data?.status.collection as RunState | undefined;
  const discovery = data?.status.discovery as RunState | undefined;
  const scheduler = data?.status.scheduler as RunState | undefined;
  return (
    <>
      <a className="skip-link" href="#results">
        跳转到趋势列表
      </a>
      <aside className="sidebar" aria-label="趋势筛选">
        <div className="brand">
          <span className="brand-mark">
            <Icon name="radar" size={24} />
          </span>
          <h1>
            GitHub <span>趋势追踪</span>
          </h1>
        </div>
        <span className="eyebrow">探索工作台</span>
        <button className="all-projects" onClick={clear}>
          全部追踪项目 <span>{data?.summary.tracked ?? "—"}</span>
        </button>
        <label>
          编程语言
          <select
            value={language}
            onChange={(e) => {
              setLanguage(e.target.value);
              setPage(1);
            }}
          >
            <option value="">全部语言</option>
            {data?.languages.map((value) => (
              <option key={value}>{value}</option>
            ))}
          </select>
        </label>
        <label>
          仓库 Topic
          <select
            value={topic}
            onChange={(e) => {
              setTopic(e.target.value);
              setPage(1);
            }}
          >
            <option value="">全部 Topics</option>
            {data?.topics.map((value) => (
              <option key={value}>{value}</option>
            ))}
          </select>
        </label>
        <div className="sidebar-note">
          <span className="status-dot" />
          共享观测，持续追踪
          <p>
            离开榜单的项目仍按小时采样。
            <br />
            历史最多保留 30 天。
          </p>
        </div>
        <div className="sidebar-tools">{tools}</div>
      </aside>
      <main>
        <div className="metrics" aria-label="数据概览">
          {[
            ["追踪项目", data?.summary.tracked, "公开数据，所有访客共享"],
            [
              "最近 2 小时已更新",
              data?.summary.fresh,
              "小时采样 · 保留真实观测",
            ],
            [
              "正在积累观测",
              data?.summary.accumulating,
              "至少两次有效快照后显示净增量",
            ],
          ].map(([label, value, note]) => (
            <article className="metric" key={label}>
              <span>{label}</span>
              <strong>{typeof value === "number" ? number(value) : "—"}</strong>
              <small>{note}</small>
            </article>
          ))}
        </div>
        <div className="section-heading">
          <h2 id="results" tabIndex={-1}>
            项目趋势 <span>{data?.total ?? "—"}</span>
          </h2>
          <button
            className="refresh"
            disabled={loading}
            onClick={() => setRefresh(refresh + 1)}
          >
            <Icon name="refresh" />
            刷新数据
          </button>
        </div>
        <div className="freshness">
          <span className="status-dot" />
          最近成功采集 {time(collection?.succeededAt)}
          {collection?.attemptedAt ? (
            <span> · 最近尝试 {time(collection.attemptedAt)}</span>
          ) : null}
        </div>
        {scheduler?.failed || collection?.failed || discovery?.failed ? (
          <div className="notice" role="status">
            更新异常：
            {scheduler?.error || collection?.error || discovery?.error}
            。继续展示上次有效观测，后台将重试。
          </div>
        ) : null}
        {data && data.summary.fresh < data.summary.tracked && (
          <div className="notice" role="status">
            {data.summary.tracked - data.summary.fresh} 个项目已超过 2
            小时未成功更新，已标记为数据过期。
          </div>
        )}
        <div className="filterbar">
          <label className="search">
            <span className="sr-only">搜索仓库名称和简介</span>
            <Icon name="search" />
            <input
              type="search"
              placeholder="搜索仓库名称或简介…"
              value={q}
              onChange={(e) => setQ(e.target.value)}
            />
          </label>
          <label className="mobile-filter">
            语言
            <select
              value={language}
              onChange={(e) => {
                setLanguage(e.target.value);
                setPage(1);
              }}
            >
              <option value="">全部语言</option>
              {data?.languages.map((value) => (
                <option key={value}>{value}</option>
              ))}
            </select>
          </label>
          <label className="mobile-filter">
            Topic
            <select
              value={topic}
              onChange={(e) => {
                setTopic(e.target.value);
                setPage(1);
              }}
            >
              <option value="">全部 Topics</option>
              {data?.topics.map((value) => (
                <option key={value}>{value}</option>
              ))}
            </select>
          </label>
          <label className="sort-control">
            <span className="sr-only">排序字段</span>
            <select
              aria-label="排序字段"
              value={sort}
              onChange={(e) => {
                setSort(e.target.value);
                setPage(1);
              }}
            >
              <option value="name">仓库名称</option>
              <option value="stars">Star 总数</option>
              {WINDOWS.map((hours) => (
                <option key={hours} value={hours}>
                  {labels[hours]}净增量
                </option>
              ))}
            </select>
          </label>
          <button
            aria-label={`当前${direction === "desc" ? "降序" : "升序"}，切换排序方向`}
            onClick={() => {
              setDirection(direction === "desc" ? "asc" : "desc");
              setPage(1);
            }}
          >
            {direction === "desc" ? "↓ 降序" : "↑ 升序"}
          </button>
        </div>
        {(search || language || topic) && (
          <p className="active-filters">
            {[search && `搜索：${search}`, language, topic]
              .filter(Boolean)
              .join(" · ")}{" "}
            <button onClick={clear}>清除筛选</button>
          </p>
        )}
        <div aria-live="polite" className="request-status">
          {loading
            ? "正在读取趋势数据…"
            : data
              ? `共 ${data.total} 个项目，第 ${data.page} / ${data.pages} 页`
              : ""}
        </div>
        {error && (
          <div className="notice error" role="alert">
            {error}{" "}
            <button onClick={() => setRefresh(refresh + 1)}>重试</button>
          </div>
        )}
        <div className="results-card" aria-busy={loading}>
          {data?.items.length ? (
            <table>
              <caption className="sr-only">
                GitHub 仓库的 Star 总数与四个滚动趋势窗口
              </caption>
              <thead>
                <tr>
                  {[
                    ["name", "仓库"],
                    ["stars", "Star"],
                    ...WINDOWS.map((h) => [String(h), `${labels[h]}趋势`]),
                  ].map(([field, label]) => (
                    <th
                      key={field}
                      scope="col"
                      aria-sort={
                        sort === field
                          ? direction === "desc"
                            ? "descending"
                            : "ascending"
                          : "none"
                      }
                    >
                      <button onClick={() => changeSort(field)}>
                        {label}{" "}
                        {sort === field
                          ? direction === "desc"
                            ? "↓"
                            : "↑"
                          : "↕"}
                      </button>
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {data.items.map((project) => (
                  <Fragment key={project.id}>
                    <tr
                      className={`repo-row ${expanded === project.id ? "is-expanded" : ""}`}
                    >
                      <td className="repository">
                        <button
                          className="expand"
                          aria-label={`${expanded === project.id ? "收起" : "展开"} ${project.name} 详情`}
                          aria-expanded={expanded === project.id}
                          aria-controls={
                            expanded === project.id
                              ? `details-${project.id}`
                              : undefined
                          }
                          onClick={() =>
                            setExpanded(
                              expanded === project.id ? null : project.id,
                            )
                          }
                        >
                          <Icon name="chevron" />
                        </button>
                        <div>
                          <a
                            className="repo-link"
                            href={`https://github.com/${project.name}`}
                            target="_blank"
                            rel="noreferrer"
                          >
                            {project.name}
                          </a>
                          <p>
                            {project.language || "语言未提供"}
                            {project.stale ? " · 数据过期" : ""}
                            {project.error ? " · 更新异常" : ""}
                          </p>
                        </div>
                      </td>
                      <td className="stars">
                        <span className="mobile-only">Star 总数 </span>
                        {number(project.stars)}
                      </td>
                      {WINDOWS.map((hours) => (
                        <td key={hours} className="trend-cell">
                          <span className="mobile-only window-label">
                            {labels[hours]}
                          </span>
                          <TrendCell trend={project.trends[hours]} />
                        </td>
                      ))}
                    </tr>
                    {expanded === project.id && (
                      <tr className="detail-row">
                        <td colSpan={6}>
                          <Details project={project} />
                        </td>
                      </tr>
                    )}
                  </Fragment>
                ))}
              </tbody>
            </table>
          ) : (
            <div className="empty">
              <Icon name="radar" size={38} />
              <h3>
                {loading
                  ? "正在连接观测数据"
                  : error
                    ? "暂时无法读取数据"
                    : data?.summary.tracked
                      ? "没有匹配的项目"
                      : "暂无追踪项目"}
              </h3>
              <p>
                {loading
                  ? "请稍候…"
                  : data?.summary.tracked
                    ? "试试其他关键词、语言或 Topic。"
                    : "后台采集后，项目会自动出现在这里。"}
              </p>
              {data?.summary.tracked ? (
                <button onClick={clear}>清除筛选</button>
              ) : null}
            </div>
          )}
        </div>
        {data && data.total > 0 && (
          <nav className="pagination" aria-label="项目列表分页">
            <span>每页 50 项 · 共 {number(data.total)} 项</span>
            <div>
              <button
                disabled={loading || data.page <= 1}
                onClick={() => setPage(data.page - 1)}
              >
                上一页
              </button>
              <span>
                {data.page} / {data.pages}
              </span>
              <button
                disabled={loading || data.page >= data.pages}
                onClick={() => setPage(data.page + 1)}
              >
                下一页
              </button>
            </div>
          </nav>
        )}
        <p className="page-note">
          净增量基于实际观测区间。部分观测参与排序；积累中始终排在最后。刷新只查询已采集结果。
        </p>
      </main>
      <div className="mobile-tools">{tools}</div>
      <dialog ref={dialog} aria-labelledby="about-title">
        <h2 id="about-title">关于这些数据</h2>
        <p>
          每 10 分钟从 GitHub Trending RSS 发现项目，持续按小时记录 Star
          总数。所有访客共享同一份数据，无需登录。
        </p>
        <p>
          四个窗口分别为滚动 24、72、168、720
          小时，以最新成功观测为终点，只使用最近 30
          天内的实际快照。端点允许约一小时偏差。
        </p>
        <p>
          历史不足显示“部分观测”和实际区间；不足两次快照显示“积累中”。负增量原样展示，缺测区间断开曲线，不补零或推算。超过
          2 小时未更新标为数据过期。
        </p>
        <p>
          每日北京时间 04:00 按近期增长优先的保留评分清理，目标保留 1000
          项。入库未满 24 小时、资料过期或观测不足的项目暂缓淘汰，因此可能超额。
        </p>
        <form method="dialog">
          <button>知道了</button>
        </form>
      </dialog>
    </>
  );
}
createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
