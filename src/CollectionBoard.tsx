import type React from "react";
import { Fragment } from "react";
import type { ListResult } from "../shared/contracts.ts";
import { Icon } from "./Icons.tsx";
import type { CollectionOverview, SourceRowView } from "./lib/collection.ts";
import { formatNumber, formatTime } from "./lib/format.ts";

function LanguageProgress({
  progress,
}: {
  progress: NonNullable<SourceRowView["languageProgress"]>;
}) {
  return (
    <div className="source-lang-progress">
      <div className="source-lang-progress-meta">
        <span>分语言进度</span>
        <strong>
          {formatNumber(progress.completed)} / {formatNumber(progress.total)}
        </strong>
      </div>
      <div className="source-lang-bar" aria-hidden="true">
        <span style={{ width: `${progress.percent}%` }} />
      </div>
    </div>
  );
}

export function CollectionBoard({
  tab,
  data,
  overview,
  loading,
  refreshing,
  error,
  isMobile,
  boardRef,
  onBack,
  onRefresh,
}: {
  tab: "github" | "linuxdo";
  data: ListResult | null;
  overview: CollectionOverview;
  loading: boolean;
  refreshing: boolean;
  error: string;
  isMobile: boolean;
  boardRef: React.RefObject<HTMLElement | null>;
  onBack: () => void;
  onRefresh: () => void;
}) {
  const tabLabel = tab === "linuxdo" ? "Linux.do" : "GitHub";
  const busy = loading || refreshing;

  const overallBadge =
    loading && !data
      ? { kind: "unrun", label: "读取中" }
      : error
        ? { kind: "failed", label: data ? "刷新失败" : "读取失败" }
        : overview.hasNotice
          ? {
              kind:
                overview.schedulerFailure ||
                overview.sampling.failedCount > 0 ||
                overview.sources.some((source) => source.health === "failed")
                  ? "failed"
                  : "partial",
              label: "存在异常",
            }
          : { kind: "ok", label: "状态正常" };

  return (
    <section
      id="collection-board"
      ref={boardRef}
      tabIndex={-1}
      className="collection-board"
      aria-labelledby="collection-board-title"
      aria-busy={busy}
    >
      <div className="collection-board-header">
        <div className="collection-board-heading">
          <button
            type="button"
            className="collection-back-button"
            onClick={onBack}
          >
            <Icon name="previous" size={16} />
            <span>返回榜单</span>
          </button>
          <div className="collection-title-group">
            <div className="collection-title-line">
              <h2 id="collection-board-title">{tabLabel} 采集看板</h2>
              <span className={`collection-badge is-${overallBadge.kind}`}>
                {overallBadge.label}
              </span>
            </div>
            <p className="collection-generated-at">
              数据生成于{" "}
              <span className="tabular">
                {data ? formatTime(data.asOf) : loading ? "读取中…" : "—"}
              </span>
            </p>
          </div>
        </div>

        <button
          type="button"
          className="collection-refresh-button"
          onClick={onRefresh}
          disabled={busy}
          aria-busy={busy}
        >
          <Icon name="reset" size={15} />
          <span>{busy ? "刷新中…" : "刷新数据"}</span>
        </button>
      </div>

      <div className="collection-board-body" aria-live="polite">
        {loading && !data ? (
          <div className="collection-panel collection-state-panel">
            <strong>正在读取采集状态…</strong>
            <p>正在获取 {tabLabel} 追踪类型的最新采集快照与趋势源状态。</p>
          </div>
        ) : error && !data ? (
          <div className="notice error collection-error-notice" role="alert">
            <span>暂时无法获取采集状态：{error}</span>
            <button type="button" onClick={onRefresh}>
              重试
            </button>
          </div>
        ) : data ? (
          <>
            {error && (
              <div className="notice error collection-error-notice" role="alert">
                <span>刷新失败（{error}），当前展示上次读取的数据。</span>
                <button type="button" onClick={onRefresh}>
                  重试
                </button>
              </div>
            )}

            {overview.alerts.length > 0 && (
              <section className="collection-alerts" aria-label="异常提示">
                <ul>
                  {overview.alerts.map((message) => (
                    <li key={message}>{message}</li>
                  ))}
                </ul>
              </section>
            )}

            {!overview.hasNotice && !error && (
              <p className="collection-status-ok" role="status">
                暂无采集异常提示。
              </p>
            )}

            <div className="collection-overview" aria-label="采集总览">
              <article className="collection-metric-card">
                <div className="collection-card-head">
                  <h3>最近 Star 采样</h3>
                  <span
                    className={`collection-badge is-${
                      overview.sampling.failedCount > 0
                        ? "failed"
                        : overview.sampling.hasRun
                          ? "ok"
                          : "unrun"
                    }`}
                  >
                    {overview.sampling.failedCount > 0
                      ? "有失败"
                      : overview.sampling.hasRun
                        ? "已完成"
                        : "尚未执行"}
                  </span>
                </div>
                <div className="collection-kpi-pair">
                  <div>
                    <span className="collection-kpi-label">成功</span>
                    <strong className="collection-kpi-value tabular">
                      {overview.sampling.succeededText}
                    </strong>
                  </div>
                  <div>
                    <span className="collection-kpi-label">失败</span>
                    <strong
                      className={`collection-kpi-value tabular${
                        overview.sampling.failedCount > 0 ? " is-negative" : ""
                      }`}
                    >
                      {overview.sampling.failedText}
                    </strong>
                  </div>
                </div>
                <dl className="collection-card-dl">
                  <div>
                    <dt>最近尝试</dt>
                    <dd className="tabular">{overview.sampling.attemptedAtText}</dd>
                  </div>
                  <div>
                    <dt>最近成功</dt>
                    <dd className="tabular">{overview.sampling.succeededAtText}</dd>
                  </div>
                  <div>
                    <dt>数据新鲜度</dt>
                    <dd className="tabular">
                      {formatNumber(data.summary.fresh)} /{" "}
                      {formatNumber(data.summary.tracked)} 正常
                      {overview.staleCount > 0
                        ? `（${formatNumber(overview.staleCount)} 项过期）`
                        : ""}
                    </dd>
                  </div>
                </dl>
              </article>

              <article className="collection-metric-card">
                <div className="collection-card-head">
                  <h3>候选处理</h3>
                  <span
                    className={`collection-badge is-${
                      overview.admission.failedCount > 0
                        ? "partial"
                        : overview.admission.hasRun
                          ? "ok"
                          : "unrun"
                    }`}
                  >
                    {overview.admission.failedCount > 0
                      ? "有待重试"
                      : overview.admission.hasRun
                        ? "已完成"
                        : "尚未执行"}
                  </span>
                </div>
                <div className="collection-kpi-pair">
                  <div>
                    <span className="collection-kpi-label">处理完成</span>
                    <strong className="collection-kpi-value tabular">
                      {overview.admission.succeededText}
                    </strong>
                  </div>
                  <div>
                    <span className="collection-kpi-label">失败待重试</span>
                    <strong
                      className={`collection-kpi-value tabular${
                        overview.admission.failedCount > 0 ? " is-warning" : ""
                      }`}
                    >
                      {overview.admission.failedText}
                    </strong>
                  </div>
                </div>
                <dl className="collection-card-dl">
                  <div>
                    <dt>最近尝试</dt>
                    <dd className="tabular">{overview.admission.attemptedAtText}</dd>
                  </div>
                  <div>
                    <dt>最近成功</dt>
                    <dd className="tabular">{overview.admission.succeededAtText}</dd>
                  </div>
                </dl>
                <p className="collection-card-note">
                  处理完成表示候选已结束处理（含确认不可访问等），并非入库数；实际入库见下方各趋势源「项目入库数」。
                </p>
              </article>

              <article className="collection-metric-card">
                <div className="collection-card-head">
                  <h3>最近淘汰</h3>
                  <span
                    className={`collection-badge is-${
                      overview.cleanup.hasRun ? "ok" : "unrun"
                    }`}
                  >
                    {overview.cleanup.hasRun ? "已执行" : "尚未执行"}
                  </span>
                </div>
                <div className="collection-kpi-single">
                  <span className="collection-kpi-label">上轮淘汰数量</span>
                  <strong className="collection-kpi-value tabular">
                    {overview.cleanup.hasRun
                      ? `${overview.cleanup.removedText} 个`
                      : "—"}
                  </strong>
                </div>
                <dl className="collection-card-dl">
                  <div>
                    <dt>淘汰时间</dt>
                    <dd className="tabular">{overview.cleanup.atText}</dd>
                  </div>
                  <div>
                    <dt>剩余追踪</dt>
                    <dd className="tabular">
                      {overview.cleanup.hasRun
                        ? `${overview.cleanup.remainingText} 个`
                        : "—"}
                    </dd>
                  </div>
                  <div>
                    <dt>参与评估</dt>
                    <dd className="tabular">
                      {overview.cleanup.hasRun
                        ? `${overview.cleanup.eligibleText} 个`
                        : "—"}
                    </dd>
                  </div>
                </dl>
              </article>
            </div>

            <section
              className="collection-panel"
              aria-labelledby="collection-sources-title"
            >
              <div className="collection-section-head">
                <div className="collection-section-title">
                  <h3 id="collection-sources-title">趋势源采集情况</h3>
                  <span className="collection-count">
                    {formatNumber(overview.sources.length)} 个来源
                  </span>
                </div>
                <p className="collection-section-note">
                  解析结果数可能包含重复或已收录项目，不代表新增追踪项目数；HelloGitHub
                  的解析结果数为待解析的详情链接数。
                </p>
              </div>

              {overview.sources.length === 0 ? (
                <p className="collection-empty-note">当前追踪类型暂无趋势源记录。</p>
              ) : isMobile ? (
                <ul className="collection-source-cards">
                  {overview.sources.map((source) => (
                    <li key={source.id} className="collection-source-card">
                      <div className="collection-source-card-head">
                        <strong>{source.name}</strong>
                        <span className={`collection-badge is-${source.health}`}>
                          {source.statusLabel}
                        </span>
                      </div>
                      {source.languageProgress && (
                        <LanguageProgress progress={source.languageProgress} />
                      )}
                      <dl className="collection-source-dl">
                        <div>
                          <dt>解析结果数</dt>
                          <dd className="tabular">{source.parsedCountText}</dd>
                        </div>
                        <div>
                          <dt>项目入库数</dt>
                          <dd className="tabular">{source.admittedCountText}</dd>
                        </div>
                        <div>
                          <dt>最近尝试</dt>
                          <dd className="tabular">{source.attemptedAtText}</dd>
                        </div>
                        <div>
                          <dt>最近成功</dt>
                          <dd className="tabular">{source.succeededAtText}</dd>
                        </div>
                        <div>
                          <dt>{source.nextAtLabel}</dt>
                          <dd className="tabular">{source.nextAtText}</dd>
                        </div>
                      </dl>
                      {source.errorMessage && (
                        <p className="collection-source-error" role="status">
                          错误原因：{source.errorMessage}
                        </p>
                      )}
                    </li>
                  ))}
                </ul>
              ) : (
                <div className="collection-sources-table-wrap">
                  <table className="collection-sources-table">
                    <thead>
                      <tr>
                        <th scope="col">趋势源</th>
                        <th scope="col">状态</th>
                        <th scope="col" className="numeric">
                          解析结果数
                        </th>
                        <th scope="col" className="numeric">
                          项目入库数
                        </th>
                        <th scope="col">最近尝试</th>
                        <th scope="col">最近成功</th>
                        <th scope="col">下次执行 / 重试</th>
                      </tr>
                    </thead>
                    <tbody>
                      {overview.sources.map((source) => (
                        <Fragment key={source.id}>
                          <tr
                            className={
                              source.errorMessage ? "has-error-row" : undefined
                            }
                          >
                            <td className="source-name-cell">
                              <strong>{source.name}</strong>
                              {source.languageProgress && (
                                <LanguageProgress
                                  progress={source.languageProgress}
                                />
                              )}
                            </td>
                            <td className="source-status-cell">
                              <span
                                className={`collection-badge is-${source.health}`}
                              >
                                {source.statusLabel}
                              </span>
                            </td>
                            <td className="numeric tabular">
                              {source.parsedCountText}
                            </td>
                            <td className="numeric tabular">
                              {source.admittedCountText}
                            </td>
                            <td className="tabular source-time-cell">
                              {source.attemptedAtText}
                            </td>
                            <td className="tabular source-time-cell">
                              {source.succeededAtText}
                            </td>
                            <td className="tabular source-time-cell">
                              <span className="source-next-cell">
                                {source.nextAtLabel === "下次重试" && (
                                  <span className="source-retry-tag">下次重试</span>
                                )}
                                <span>{source.nextAtText}</span>
                              </span>
                            </td>
                          </tr>
                          {source.errorMessage && (
                            <tr className="collection-source-error-row">
                              <td colSpan={7}>
                                <p className="collection-source-error" role="status">
                                  错误原因：{source.errorMessage}
                                </p>
                              </td>
                            </tr>
                          )}
                        </Fragment>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </section>

            <section
              className="collection-panel"
              aria-labelledby="collection-problems-title"
            >
              <div className="collection-section-head">
                <div className="collection-section-title">
                  <h3 id="collection-problems-title">异常项目</h3>
                  <span className="collection-count">
                    {formatNumber(overview.problemProjects.length)}
                  </span>
                </div>
                {overview.problemProjects.length > 0 && (
                  <p className="collection-section-note">
                    点击项目展开查看异常原因、最近采样尝试与最后成功观测时间。
                  </p>
                )}
              </div>

              {overview.problemProjects.length === 0 ? (
                <p className="collection-empty-note">当前暂无异常项目。</p>
              ) : (
                <ul className="collection-problem-list" aria-label="异常项目列表">
                  {overview.problemProjects.map((item) => (
                    <li key={item.project.id}>
                      <details className="collection-problem-item">
                        <summary className="collection-problem-summary">
                          <span
                            className="collection-problem-chevron"
                            aria-hidden="true"
                          >
                            <Icon name="next" size={14} />
                          </span>
                          <span className="collection-problem-name">
                            {item.project.name}
                          </span>
                          <span className="collection-problem-tags">
                            {item.tags.map((tag) => (
                              <span
                                key={tag.kind}
                                className={`collection-badge is-${
                                  tag.kind === "error" ? "failed" : "partial"
                                }`}
                              >
                                {tag.label}
                              </span>
                            ))}
                          </span>
                        </summary>
                        <div className="collection-problem-body">
                          <ul className="project-problem-reasons">
                            {item.reasons.map((reason) => (
                              <li key={reason}>{reason}</li>
                            ))}
                          </ul>
                          <dl className="collection-problem-meta">
                            <div>
                              <dt>最近采样尝试</dt>
                              <dd className="tabular">{item.attemptedAtText}</dd>
                            </div>
                            <div>
                              <dt>最后成功观测</dt>
                              <dd className="tabular">{item.observedAtText}</dd>
                            </div>
                            {item.unavailableAtText && (
                              <div>
                                <dt>确认不可访问</dt>
                                <dd className="tabular">
                                  {item.unavailableAtText}
                                </dd>
                              </div>
                            )}
                          </dl>
                        </div>
                      </details>
                    </li>
                  ))}
                </ul>
              )}
            </section>
          </>
        ) : null}
      </div>
    </section>
  );
}
