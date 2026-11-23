import { WINDOWS, type Project, type TrendSummary } from "../shared/contracts.ts";
import {
  formatCompact,
  formatCompactSigned,
  formatHeat,
  formatNumber,
  formatSigned,
  formatTime,
  trendStateLabel,
} from "./lib/format.ts";

export function HeatValue({
  project,
  heat,
}: {
  project: Project;
  heat: number | null;
}) {
  const unknown = WINDOWS.some(
    (hours) => project.trends[hours].basis === "unknown",
  );
  return (
    <span
      className={heat === null ? "accumulating" : undefined}
      title={
        heat === null
          ? unknown
            ? "观测数据过期，暂无法计算热度"
            : "至少实际观测 1 天且四窗口均可计算后展示热度"
          : "四窗口正向日均 Star 增长，按 60%、25%、10%、5% 加权"
      }
    >
      {heat === null ? (unknown ? "—" : "积累中") : formatHeat(heat)}
    </span>
  );
}

export function TrendValue({
  trend,
  showState = false,
}: {
  trend: TrendSummary;
  showState?: boolean;
}) {
  const label = trendStateLabel(trend);
  const hint = `${label}${
    trend.start && trend.end
      ? `，${formatTime(trend.start)} 至 ${formatTime(trend.end)}`
      : ""
  }${trend.gaps.length ? `，${trend.gaps.length} 段缺测` : ""}`;
  const valueText =
    trend.basis === "unknown" ? "暂不可知" : formatSigned(trend.delta);
  return (
    <>
      <span
        className={
          trend.delta === null
            ? "accumulating"
            : `gain ${trend.delta < 0 ? "negative" : ""}`
        }
        title={`${valueText}，${hint}`}
        aria-label={`${valueText}，${hint}`}
      >
        {trend.basis === "unknown" ? "暂不可知" : formatCompactSigned(trend.delta)}
      </span>
      {showState &&
        trend.delta !== null &&
        trend.basis !== "unknown" &&
        (trend.basis === "carried" || trend.state === "partial") && (
          <span className="partial" aria-hidden="true">
            {label}
          </span>
        )}
    </>
  );
}

export function StarValue({ stars }: { stars: number }) {
  const full = formatNumber(stars);
  return (
    <span title={full} aria-label={`${full} Star`}>
      {formatCompact(stars)}
    </span>
  );
}

export function ProjectButton({
  project,
  admissionLabel,
  selected,
  onClick,
}: {
  project: Project;
  admissionLabel: string | null;
  selected: boolean;
  onClick?: () => void;
}) {
  const [owner, ...name] = project.name.split("/");
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
        <span
          className="new-admission"
          title={`新入库：${formatTime(project.enteredAt)}`}
        >
          {admissionLabel}
        </span>
      )}
    </button>
  );
}
