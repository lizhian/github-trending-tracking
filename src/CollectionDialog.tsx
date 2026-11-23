import { useRef } from "react";
import type {
  CleanupResult,
  ListResult,
  Project,
  RunState,
} from "../shared/contracts.ts";
import { ProjectProblemDetails } from "./ProjectProblems.tsx";
import { isDialogBackdrop } from "./lib/dom.ts";
import { formatNumber, formatTime } from "./lib/format.ts";

export function CollectionDialog({
  dialogRef,
  tab,
  data,
  loading,
  error,
  hasNotice,
  failure,
  discovery,
  admission,
  cleanup,
  problemProjects,
}: {
  dialogRef: React.RefObject<HTMLDialogElement | null>;
  tab: "github" | "linuxdo";
  data: ListResult | null;
  loading: boolean;
  error: string;
  hasNotice: boolean;
  failure: RunState | undefined;
  discovery: RunState | undefined;
  admission: RunState | undefined;
  cleanup: CleanupResult | undefined;
  problemProjects: Project[];
}) {
  const backdropPress = useRef(false);

  return (
    <dialog
      id="collection-messages"
      className="collection-dialog"
      ref={dialogRef}
      aria-labelledby="collection-messages-title"
      onPointerDown={(event) => {
        backdropPress.current = isDialogBackdrop(event);
      }}
      onClick={(event) => {
        const close = backdropPress.current && isDialogBackdrop(event);
        backdropPress.current = false;
        if (close) event.currentTarget.close();
      }}
    >
      <div className="collection-heading">
        <h2 id="collection-messages-title">
          {tab === "linuxdo" ? "Linux.do" : "GitHub"} 采集消息
        </h2>
        <button
          type="button"
          className="icon-button"
          aria-label="关闭采集消息"
          onClick={() => dialogRef.current?.close()}
        >
          ×
        </button>
      </div>
      <div aria-live="polite">
        {data && (
          <p>
            上轮按热度淘汰{" "}
            {cleanup ? `${formatNumber(cleanup.removed)} 个项目` : "—"}
          </p>
        )}
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
        {!!admission?.failed && <p>部分候选暂未收录，后台将重试。</p>}
        {data && data.summary.fresh < data.summary.tracked && (
          <p>
            {data.summary.tracked - data.summary.fresh} 个项目未按计划完成更新，已超过 10
            分钟宽限并标记为数据过期。
          </p>
        )}
        {!!data?.sources?.length && (
          <section
            className="collection-sources"
            aria-labelledby="collection-sources-title"
          >
            <h3 id="collection-sources-title">趋势源采集情况</h3>
            <p>
              解析结果可能包含已收录或重复项目；HelloGitHub 的解析结果为待解析的详情链接。
            </p>
            <ul className="collection-source-list">
              {data.sources.map((source) => (
                <li key={source.id}>
                  <strong>{source.name}</strong>
                  <span className={source.failed ? "source-failed" : ""}>
                    {source.failed
                      ? "异常，等待重试"
                      : !source.attemptedAt
                        ? "尚未执行"
                        : source.languagesTotal &&
                            source.languagesCompleted !== source.languagesTotal
                          ? "部分完成"
                          : "成功"}
                  </span>
                  <dl>
                    <dt>最近尝试</dt>
                    <dd>
                      {source.attemptedAt
                        ? formatTime(source.attemptedAt)
                        : "尚未执行"}
                    </dd>
                    <dt>最近成功</dt>
                    <dd>{formatTime(source.succeededAt)}</dd>
                    <dt>解析结果数</dt>
                    <dd>
                      {source.attemptedAt ? formatNumber(source.succeeded) : "—"}
                    </dd>
                    <dt>项目入库数</dt>
                    <dd>
                      {source.admitted == null
                        ? "—"
                        : formatNumber(source.admitted)}
                    </dd>
                    {source.languagesTotal && (
                      <>
                        <dt>已完成语言</dt>
                        <dd>
                          {source.languagesCompleted} / {source.languagesTotal}
                        </dd>
                      </>
                    )}
                    <dt>{source.retryAfter ? "下次重试" : "下次执行"}</dt>
                    <dd>
                      {!source.nextAt
                        ? "等待调度"
                        : source.nextAt <= data.asOf
                          ? "已到期，等待调度"
                          : formatTime(source.nextAt)}
                    </dd>
                  </dl>
                  {source.error && (
                    <p role="status">
                      {source.error === "GitHub Search incomplete results"
                        ? "搜索结果不完整，已保留返回的候选，将重试。"
                        : source.error}
                    </p>
                  )}
                </li>
              ))}
            </ul>
          </section>
        )}
        {problemProjects.length > 0 && (
          <section
            className="collection-project-problems"
            aria-labelledby="project-problems-title"
          >
            <h3 id="project-problems-title">
              有问题的项目（{problemProjects.length}）
            </h3>
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
        {!hasNotice && (
          <p>
            {loading
              ? "正在读取采集状态…"
              : error
                ? "暂时无法获取采集状态。"
                : "暂无采集异常提示。"}
          </p>
        )}
      </div>
    </dialog>
  );
}
