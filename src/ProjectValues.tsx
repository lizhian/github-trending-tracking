import { useEffect, useId, useMemo, useRef, useState, type CSSProperties } from "react";
import type { Project, TrendSummary } from "../shared/contracts.ts";
import { projectHeatBreakdown, type HeatWindowBreakdown } from "./heat.ts";
import {
  formatCompact,
  formatCompactSigned,
  formatHeat,
  formatNumber,
  formatSigned,
  formatTime,
  trendStateLabel,
  WINDOW_LABELS,
} from "./lib/format.ts";

function formatHeatDays(actualDays: number): string {
  if (actualDays < 1) {
    const hours = Math.round(actualDays * 24 * 10) / 10;
    return `1天(${hours < 1 ? "<1h" : `${hours}h`})`;
  }
  const rounded = Math.round(actualDays * 10_000) / 10_000;
  return `${rounded}天`;
}

function formatHeatContribution(value: number): string {
  if (value > 0 && value < 0.05) return "<0.1";
  return formatHeat(value);
}

function formatHeatDetail(item: HeatWindowBreakdown): string {
  if (item.status === "stale") return "观测过期";
  if (item.status === "unknown") return "净增量未知";
  if (item.status === "missing") return "缺观测";
  if (item.delta === null || item.actualDays === null) return "缺观测";
  if (item.delta < 0) return `${formatSigned(item.delta)} → 0`;
  return `${formatSigned(item.delta)} ÷ ${formatHeatDays(item.actualDays)}`;
}

export function HeatValue({
  project,
  heat,
}: {
  project: Project;
  heat: number;
}) {
  const id = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const popup = useRef<HTMLDivElement>(null);
  const pinned = useRef(false);
  const pointerOnTrigger = useRef(false);
  const pointerOnPopup = useRef(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<CSSProperties>({});

  const cancelHide = () => clearTimeout(timer.current);
  const hide = () => {
    cancelHide();
    const el = popup.current;
    if (!el) return;
    if (typeof el.hidePopover === "function") {
      if (el.matches(":popover-open")) el.hidePopover();
    } else {
      pinned.current = false;
      setOpen(false);
    }
  };

  const show = () => {
    cancelHide();
    const rect = trigger.current?.getBoundingClientRect();
    const el = popup.current;
    if (!rect || !el) return;
    const width = Math.min(312, window.innerWidth - 24);
    const spaceBelow = window.innerHeight - rect.bottom - 16;
    const spaceAbove = rect.top - 16;
    const below = spaceBelow >= 230 || spaceBelow >= spaceAbove;
    setPosition({
      width,
      left: Math.max(12, Math.min(rect.right - width, window.innerWidth - width - 12)),
      top: below ? rect.bottom + 8 : "auto",
      bottom: below ? "auto" : window.innerHeight - rect.top + 8,
      maxHeight: Math.max(140, Math.min(320, below ? spaceBelow : spaceAbove)),
    });
    if (typeof el.showPopover === "function") {
      if (!el.matches(":popover-open")) el.showPopover();
    } else {
      setOpen(true);
    }
  };

  const scheduleHide = () => {
    cancelHide();
    if (!pinned.current) {
      timer.current = setTimeout(() => {
        if (
          !pinned.current && !pointerOnTrigger.current && !pointerOnPopup.current &&
          document.activeElement !== trigger.current
        ) {
          hide();
        }
      }, 180);
    }
  };

  useEffect(() => () => clearTimeout(timer.current), []);

  useEffect(() => {
    if (!open) return;
    const dismiss = (event: Event) => {
      if (event.target instanceof Node && popup.current?.contains(event.target)) {
        return;
      }
      hide();
    };
    window.addEventListener("scroll", dismiss, true);
    window.addEventListener("resize", dismiss);
    return () => {
      clearTimeout(timer.current);
      window.removeEventListener("scroll", dismiss, true);
      window.removeEventListener("resize", dismiss);
    };
  }, [open]);

  const breakdown = useMemo(
    () => (open ? projectHeatBreakdown(project) : null),
    [open, project],
  );
  const formatted = formatHeat(heat);

  return (
    <>
      <button
        type="button"
        ref={trigger}
        className="heat-trigger"
        aria-label={`热度 ${formatted}，查看计算明细`}
        aria-expanded={open}
        aria-controls={id}
        onPointerEnter={(event) => {
          if (event.pointerType === "mouse") {
            pointerOnTrigger.current = true;
            show();
          }
        }}
        onPointerLeave={(event) => {
          if (event.pointerType === "mouse") {
            pointerOnTrigger.current = false;
            scheduleHide();
          }
        }}
        onFocus={show}
        onKeyDown={(event) => {
          if (event.key === "Escape" && open) {
            event.preventDefault();
            hide();
          }
        }}
        onBlur={(event) => {
          const next = event.relatedTarget;
          if (
            pointerOnPopup.current ||
            (next instanceof Node && popup.current?.contains(next))
          ) {
            return;
          }
          pinned.current = false;
          if (!pointerOnTrigger.current) hide();
        }}
        onClick={(event) => {
          event.stopPropagation();
          if (pinned.current) {
            pinned.current = false;
            hide();
          } else {
            pinned.current = true;
            show();
          }
        }}
      >
        {formatted}
      </button>
      <div
        id={id}
        ref={popup}
        popover="auto"
        role="tooltip"
        className="heat-tooltip"
        data-open={open ? "true" : undefined}
        style={position}
        onPointerEnter={(event) => {
          if (event.pointerType === "mouse") {
            pointerOnPopup.current = true;
            cancelHide();
          }
        }}
        onPointerLeave={(event) => {
          if (event.pointerType === "mouse") {
            pointerOnPopup.current = false;
            scheduleHide();
          }
        }}
        onClick={(event) => event.stopPropagation()}
        onToggle={() => {
          const visible = popup.current?.matches(":popover-open") ?? false;
          setOpen(visible);
          if (!visible) {
            pinned.current = false;
            pointerOnPopup.current = false;
            cancelHide();
          }
        }}
      >
        {breakdown && (
          <>
            <div className="heat-tooltip-head">
              <strong>热度计算</strong>
              <span className="heat-tooltip-total">
                {formatHeat(breakdown.total)}
              </span>
            </div>
            <p className="heat-tooltip-formula">
              Σ [max(Star 净增量, 0) ÷ max(观测天数, 1) × 权重]
            </p>
            <p className="heat-tooltip-note">
              短于 1 天按 1 天计。缺观测或过期窗口计 0。
            </p>
            <ul className="heat-tooltip-list">
              {breakdown.windows.map((item) => {
                const detail = formatHeatDetail(item);
                const statusClass =
                  item.status === "stale" || item.status === "unknown"
                    ? " is-stale"
                    : item.status === "missing"
                      ? " is-missing"
                      : item.contribution === 0
                        ? " is-zero"
                        : "";
                return (
                  <li key={item.hours} className="heat-tooltip-row">
                    <span className="heat-tooltip-window">
                      {WINDOW_LABELS[item.hours]} · {Math.round(item.weight * 100)}%
                    </span>
                    <span className={`heat-tooltip-detail${statusClass}`}>
                      {detail}
                    </span>
                    <span className={`heat-tooltip-score${statusClass}`}>
                      {formatHeatContribution(item.contribution)}
                    </span>
                  </li>
                );
              })}
            </ul>
          </>
        )}
      </div>
    </>
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
