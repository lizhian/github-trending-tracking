import type {
  CleanupResult,
  ListResult,
  Project,
  RunState,
  SourceStatus,
} from "../../shared/contracts.ts";
import { formatNumber, formatTime } from "./format.ts";

type SourceHealth = "ok" | "partial" | "failed" | "unrun";

export interface SourceRowView {
  id: string;
  name: string;
  health: SourceHealth;
  statusLabel: string;
  attemptedAtText: string;
  succeededAtText: string;
  parsedCountText: string;
  admittedCountText: string;
  nextAtLabel: "下次重试" | "下次执行";
  nextAtText: string;
  languageProgress: { completed: number; total: number; percent: number } | null;
  errorMessage: string | null;
}

interface RunSummaryView {
  hasRun: boolean;
  succeededText: string;
  failedText: string;
  failedCount: number;
  attemptedAtText: string;
  succeededAtText: string;
  error: string | null;
}

interface CleanupSummaryView {
  hasRun: boolean;
  removedText: string;
  remainingText: string;
  eligibleText: string;
  atText: string;
}

interface ProblemTag {
  kind: "unavailable" | "stale" | "error";
  label: string;
}

interface ProblemProjectView {
  project: Project;
  tags: ProblemTag[];
  reasons: string[];
  attemptedAtText: string;
  observedAtText: string;
  unavailableAtText: string | null;
}

export interface CollectionOverview {
  sampling: RunSummaryView;
  schedulerFailure: RunState | null;
  admission: RunSummaryView;
  cleanup: CleanupSummaryView;
  staleCount: number;
  sources: SourceRowView[];
  problemProjects: ProblemProjectView[];
  hasNotice: boolean;
  alerts: string[];
}

export function projectProblems(project: Project): string[] {
  const problems: string[] = [];
  if (project.unavailableAt) {
    problems.push("仓库不可访问，按最后已知 Star 总数计算。");
  }
  if (project.stale) {
    problems.push("数据过期，未在应采时段及 10 分钟宽限内成功更新。");
  }
  if (project.error) {
    problems.push(`更新异常：${project.error}`);
  }
  return problems;
}

function formatSourceError(error: string | null | undefined): string | null {
  if (!error) return null;
  return error === "GitHub Search incomplete results"
    ? "搜索结果不完整，已保留返回的候选，将重试。"
    : error;
}

export function classifySource(source: SourceStatus, asOf: number): SourceRowView {
  const hasLanguages = Boolean(source.languagesTotal && source.languagesTotal > 0);
  const completed = source.languagesCompleted ?? 0;
  const total = source.languagesTotal ?? 0;
  const languageProgress = hasLanguages
    ? {
        completed,
        total,
        percent: Math.min(100, Math.max(0, Math.round((completed / total) * 100))),
      }
    : null;

  let health: SourceHealth;
  let statusLabel: string;

  if (!source.attemptedAt && !source.failed) {
    health = "unrun";
    statusLabel = "尚未执行";
  } else if (hasLanguages && completed === 0 && !source.failed) {
    health = "unrun";
    statusLabel = source.retryAfter ? "等待重试" : "等待采集";
  } else if (hasLanguages && completed < total) {
    if (completed === 0 && source.failed > 0) {
      health = "failed";
      statusLabel = "异常，等待重试";
    } else {
      health = "partial";
      statusLabel =
        source.failed > 0 || source.retryAfter ? "部分完成，等待重试" : "部分完成";
    }
  } else if (source.failed > 0) {
    health = "failed";
    statusLabel = "异常，等待重试";
  } else {
    health = "ok";
    statusLabel = "成功";
  }

  const nextAtLabel: "下次重试" | "下次执行" = source.retryAfter
    ? "下次重试"
    : "下次执行";
  const nextAtText = !source.nextAt
    ? "等待调度"
    : source.nextAt <= asOf
      ? "已到期，等待调度"
      : formatTime(source.nextAt);

  return {
    id: source.id,
    name: source.name,
    health,
    statusLabel,
    attemptedAtText: source.attemptedAt ? formatTime(source.attemptedAt) : "尚未执行",
    succeededAtText: formatTime(source.succeededAt),
    parsedCountText: source.attemptedAt ? formatNumber(source.succeeded) : "—",
    admittedCountText:
      source.admitted == null ? "—" : formatNumber(source.admitted),
    nextAtLabel,
    nextAtText,
    languageProgress,
    errorMessage: formatSourceError(source.error),
  };
}

function summarizeRun(run: RunState | undefined): RunSummaryView {
  const hasRun = Boolean(
    run && (run.attemptedAt > 0 || run.succeeded > 0 || run.failed > 0),
  );
  if (!hasRun || !run) {
    return {
      hasRun: false,
      succeededText: "—",
      failedText: "—",
      failedCount: 0,
      attemptedAtText: "尚未执行",
      succeededAtText: "尚无成功记录",
      error: null,
    };
  }
  return {
    hasRun: true,
    succeededText: formatNumber(run.succeeded),
    failedText: formatNumber(run.failed),
    failedCount: run.failed,
    attemptedAtText: run.attemptedAt ? formatTime(run.attemptedAt) : "尚未执行",
    succeededAtText: formatTime(run.succeededAt),
    error: run.error ?? null,
  };
}

function summarizeCleanup(cleanup: CleanupResult | undefined): CleanupSummaryView {
  if (!cleanup || !cleanup.at) {
    return {
      hasRun: false,
      removedText: "—",
      remainingText: "—",
      eligibleText: "—",
      atText: "尚未执行",
    };
  }
  return {
    hasRun: true,
    removedText: formatNumber(cleanup.removed),
    remainingText: formatNumber(cleanup.remaining),
    eligibleText: formatNumber(cleanup.eligible),
    atText: formatTime(cleanup.at),
  };
}

function buildProblemProject(project: Project): ProblemProjectView | null {
  const reasons = projectProblems(project);
  if (!reasons.length) return null;
  const tags: ProblemTag[] = [];
  if (project.unavailableAt) tags.push({ kind: "unavailable", label: "仓库不可访问" });
  if (project.stale) tags.push({ kind: "stale", label: "数据过期" });
  if (project.error) tags.push({ kind: "error", label: "更新异常" });
  return {
    project,
    tags,
    reasons,
    attemptedAtText: project.attemptedAt
      ? formatTime(project.attemptedAt)
      : "尚无尝试记录",
    observedAtText: formatTime(project.observedAt),
    unavailableAtText: project.unavailableAt
      ? formatTime(project.unavailableAt)
      : null,
  };
}

export function deriveCollectionOverview(data: ListResult | null): CollectionOverview {
  const scheduler = data?.status.scheduler as RunState | undefined;
  const collection = data?.status.collection as RunState | undefined;
  const discovery = (data?.status.discovery as RunState | undefined) ?? null;
  const admissionRaw = data?.status.admission as RunState | undefined;
  const cleanupRaw = data?.status.cleanup as CleanupResult | undefined;

  const sampling = summarizeRun(collection);
  const admission = summarizeRun(admissionRaw);
  const cleanup = summarizeCleanup(cleanupRaw);
  const schedulerFailure = scheduler?.failed ? scheduler : null;
  const staleCount = data
    ? Math.max(0, data.summary.tracked - data.summary.fresh)
    : 0;
  const sources = (data?.sources ?? []).map((source) =>
    classifySource(source, data?.asOf ?? 0),
  );
  const problemProjects: ProblemProjectView[] = [];
  for (const project of data?.items ?? []) {
    const view = buildProblemProject(project);
    if (view) problemProjects.push(view);
  }

  const alerts: string[] = [];
  if (schedulerFailure) {
    alerts.push(
      `调度异常：${schedulerFailure.error || "采集尚未恢复"}。继续展示上次有效观测，后台将重试。`,
    );
  }
  if (collection?.failed) {
    alerts.push(
      `更新异常：${collection.error || "采集尚未恢复"}。继续展示上次有效观测，后台将重试。`,
    );
  }
  if (discovery?.failed) {
    alerts.push(
      `趋势源获取异常：${discovery.error || "暂时无法获取新候选"}。后台将重试。`,
    );
  }
  if (admission.failedCount > 0) {
    alerts.push(
      `部分候选暂未收录（${admission.failedText} 项待重试）${admission.error ? `：${admission.error}` : ""}，后台将重试。`,
    );
  }
  if (staleCount > 0) {
    alerts.push(
      `${formatNumber(staleCount)} 个项目未按计划完成更新，已超过 10 分钟宽限并标记为数据过期。`,
    );
  }

  const hasSourceAttention = sources.some(
    (source) => source.health === "failed" || source.health === "partial",
  );
  const hasNotice = Boolean(
    problemProjects.length > 0 ||
      schedulerFailure ||
      collection?.failed ||
      discovery?.failed ||
      admission.failedCount > 0 ||
      staleCount > 0 ||
      hasSourceAttention,
  );

  return {
    sampling,
    schedulerFailure,
    admission,
    cleanup,
    staleCount,
    sources,
    problemProjects,
    hasNotice,
    alerts,
  };
}
