import { useState } from "react";
import {
  WINDOWS,
  type HistoryResult,
  type Project,
  type WindowHours,
} from "../shared/contracts.ts";
import { Icon } from "./Icons.tsx";
import {
  ChartPlaceholder,
  TrendChart,
  type ChartMode,
} from "./TrendChart.tsx";
import {
  formatHeat,
  formatNumber,
  formatSigned,
  formatTime,
  parseHomepageURL,
  trendStateLabel,
  WINDOW_LABELS,
} from "./lib/format.ts";
import { useApi } from "./lib/useApi.ts";

export function ProjectInspector({
  project,
  heat,
  windowHours,
  refresh,
  asOf,
  onWindow,
  onTopic,
  onRefresh,
  onBack,
}: {
  project: Project;
  heat: number | null;
  windowHours: WindowHours;
  refresh: number;
  asOf: number;
  onWindow: (hours: WindowHours) => void;
  onTopic: (topic: string) => void;
  onRefresh: () => void;
  onBack?: () => void;
}) {
  const [retry, setRetry] = useState(0);
  const [chartMode, setChartMode] = useState<ChartMode>("delta");
  const history = useApi<HistoryResult>(
    `/api/projects/${encodeURIComponent(project.id)}/history?window=${windowHours}`,
    `${project.observedAt}:${refresh}:${retry}`,
  );
  const current = history.value;
  const trend = current?.trend;
  const [owner, ...name] = project.name.split("/");
  const homepage = parseHomepageURL(project.homepage);
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
          <button
            type="button"
            className="mobile-details-back"
            aria-label="返回项目榜单"
            onClick={onBack}
          >
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
              <span className="detail-title-external">
                <Icon name="external" size={12} />
              </span>
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
                width="16"
                height="16"
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
          {summary && (
            <p className="detail-description" aria-label="AI 项目总结">
              {summary}
            </p>
          )}
          {aiTags.length > 0 && (
            <div className="detail-ai-tags" aria-label="AI 项目标签">
              {aiTags.map((tag) => (
                <button
                  type="button"
                  className="ai-tag"
                  key={tag}
                  onClick={() => onTopic(tag)}
                  aria-label={`搜索 AI 标签 ${tag}`}
                >
                  {tag}
                </button>
              ))}
            </div>
          )}
        </div>
      )}

      {!!(current?.unavailableAt ?? project.unavailableAt) && (
        <p className="notice" role="status">
          仓库不可访问，按最后已知 Star 总数计算。
        </p>
      )}
      {((current?.stale ?? project.stale) || project.error) && (
        <p className="notice" role="status">
          {(current?.stale ?? project.stale)
            ? "数据过期，未在应采时段及 10 分钟宽限内成功更新。"
            : ""}
          {project.error ? "项目更新异常，继续显示有效观测。" : ""}
        </p>
      )}

      <div className="detail-stat">
        <span>Star 总数</span>
        <div className="detail-star-line">
          <strong>
            {formatNumber(current?.points.at(-1)?.[1] ?? project.stars)}
          </strong>
          {trend && !history.loading && (
            <span
              className={`delta-badge ${trend.delta !== null && trend.delta < 0 ? "negative" : ""}`}
            >
              {trend.basis === "unknown"
                ? "暂不可知"
                : formatSigned(trend.delta)}
            </span>
          )}
        </div>
      </div>

      <div
        className="history-region"
        aria-busy={history.loading || history.refreshing}
      >
        <div className="chart-heading">
          <span>
            {chartMode === "delta" ? "Star 净增量" : "Star 总数走势"}
          </span>
          <div className="chart-mode-toggle" role="group" aria-label="图表视图">
            <button
              type="button"
              aria-pressed={chartMode === "delta"}
              onClick={() => setChartMode("delta")}
            >
              净增量
            </button>
            <button
              type="button"
              aria-pressed={chartMode === "total"}
              onClick={() => setChartMode("total")}
            >
              Star 曲线
            </button>
          </div>
        </div>
        <div className="chart-stage">
          {history.loading ? (
            <ChartPlaceholder />
          ) : history.error && !current ? (
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
              <TrendChart
                trend={trend}
                mode={chartMode}
                refreshing={history.refreshing}
              />
            ) : (
              <p className="chart-message">
                {trend.points.length === 1
                  ? "至少需要两次快照才能显示区间差值"
                  : trend.basis === "carried"
                    ? "此窗口无真实观测，增量按持平计算"
                    : trend.basis === "unknown"
                      ? "此窗口暂无有效观测"
                      : "积累中"}
              </p>
            )
          ) : null}
        </div>
        <div className="periods" aria-label="趋势窗口">
          {WINDOWS.map((hours) => (
            <button
              type="button"
              key={hours}
              aria-pressed={windowHours === hours}
              onClick={() => onWindow(hours)}
            >
              {WINDOW_LABELS[hours]}
            </button>
          ))}
        </div>
        <div className="chart-feedback">
          {!history.loading &&
            !history.error &&
            current &&
            (current.gaps.length > 0 || current.stale) && (
              <p className="chart-note" role="status">
                {current.gaps.length
                  ? `${chartMode === "delta" ? "浅色柱" : "虚线段"}表示缺测区间（${current.gaps.length} 段）`
                  : ""}
                {current.stale ? " 历史数据过期" : ""}
              </p>
            )}
          {history.error && current && (
            <p className="chart-note" role="status">
              刷新失败（{history.error}），当前展示缓存结果。
            </p>
          )}
        </div>
      </div>

      <dl className="observation">
        <dt>编程语言</dt>
        <dd>{project.language || "未提供"}</dd>
        <dt>{WINDOW_LABELS[windowHours]} Star 净增量</dt>
        <dd>
          {history.loading
            ? "读取中"
            : trend
              ? trend.basis === "unknown"
                ? "暂不可知"
                : formatSigned(trend.delta)
              : "暂不可用"}
        </dd>
        <dt>热度评分</dt>
        <dd>{heat === null ? "积累中" : formatHeat(heat)}</dd>
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
                ? trendStateLabel(trend)
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
          <dd>{formatTime(project.enteredAt)}</dd>
          <dt>最后成功观测</dt>
          <dd>{formatTime(current?.observedAt ?? project.observedAt)}</dd>
        </dl>
      </section>

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
