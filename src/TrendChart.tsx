import { useEffect, useMemo, useRef, useState } from "react";
import type { Trend } from "../shared/contracts.ts";
import {
  formatChartAxisDate,
  formatChartIntervalTime,
  formatChartPointTime,
  formatCompact,
  formatNumber,
  formatSigned,
  trendStateLabel,
} from "./lib/format.ts";

export type ChartMode = "delta" | "total";

export function ChartPlaceholder() {
  return (
    <div className="chart-placeholder">
      <div className="chart-detail chart-detail-hint">正在读取历史走势…</div>
      <svg
        className="chart chart-skeleton"
        viewBox="0 0 288 128"
        aria-hidden="true"
      >
        {[20, 56, 92].map((y) => (
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
          y="113"
          width="32"
          height="6"
          rx="2"
        />
        <rect
          className="skeleton-mark"
          x="200"
          y="113"
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

const PLOT_LEFT = 4;
const PLOT_RIGHT = 232;
const PLOT_WIDTH = PLOT_RIGHT - PLOT_LEFT;
const PLOT_TOP = 16;
const PLOT_BOTTOM = 94;
const PLOT_MID = (PLOT_TOP + PLOT_BOTTOM) / 2;
const PLOT_HEIGHT = PLOT_BOTTOM - PLOT_TOP;

export function TrendChart({
  trend,
  mode,
  refreshing = false,
}: {
  trend: Trend;
  mode: ChartMode;
  refreshing?: boolean;
}) {
  const [activeIndex, setActiveIndex] = useState<number | null>(null);
  const svgRef = useRef<SVGSVGElement>(null);
  const itemRefs = useRef<(SVGRectElement | SVGCircleElement | null)[]>([]);

  useEffect(() => {
    setActiveIndex(null);
  }, [trend, mode]);

  const points = trend.points;
  const first = points[0][0];
  const last = points[points.length - 1][0];
  const span = last - first || 1;
  const xAt = (at: number) => PLOT_LEFT + ((at - first) / span) * PLOT_WIDTH;

  const intervals = useMemo(
    () =>
      points.slice(1).map(([to, stars], index) => {
        const [from, previousStars] = points[index];
        return {
          from,
          to,
          delta: stars - previousStars,
          missing: trend.gaps.some(
            ([start, end]) => from === start && to === end,
          ),
          ...formatChartIntervalTime(from, to),
        };
      }),
    [points, trend.gaps],
  );

  const deltaScale = useMemo(() => {
    const values = intervals.map(({ delta }) => delta);
    const smallest = Math.min(0, ...values);
    const largest = Math.max(0, ...values);
    const mixed = smallest < 0 && largest > 0;
    const magnitude = Math.max(-smallest, largest);
    const min = mixed ? -magnitude : smallest;
    const max = mixed ? magnitude : largest;
    const ticks = [...new Set([max, Math.round((max + min) / 2), min])];
    const tickLabels = ticks.map((v) => (v === 0 ? "0" : formatSigned(v)));
    const y = (v: number) =>
      min === max ? PLOT_MID : PLOT_BOTTOM - ((v - min) / (max - min)) * PLOT_HEIGHT;
    return {
      ticks,
      tickLabels,
      y,
      zero: y(0),
      viewWidth: Math.max(
        288,
        248 + Math.max(...tickLabels.map((l) => l.length)) * 7,
      ),
    };
  }, [intervals]);

  const totalScale = useMemo(() => {
    const values = points.map(([, stars]) => stars);
    const min = Math.min(...values);
    const max = Math.max(...values);
    const ticks =
      min === max ? [min] : [...new Set([max, Math.round((max + min) / 2), min])];
    const tickLabels = ticks.map((v) => formatCompact(v));
    const y = (v: number) =>
      min === max ? PLOT_MID : PLOT_BOTTOM - ((v - min) / (max - min)) * PLOT_HEIGHT;
    return {
      ticks,
      tickLabels,
      y,
      viewWidth: Math.max(
        288,
        248 + Math.max(...tickLabels.map((l) => l.length)) * 7,
      ),
    };
  }, [points]);

  const scale = mode === "delta" ? deltaScale : totalScale;

  const handleScrub = (clientX: number) => {
    const svg = svgRef.current;
    if (!svg) return;
    const rect = svg.getBoundingClientRect();
    if (!rect.width) return;
    const svgX = ((clientX - rect.left) / rect.width) * scale.viewWidth;
    if (svgX < PLOT_LEFT - 8 || svgX > PLOT_RIGHT + 8) return;
    if (mode === "delta") {
      if (!intervals.length) return;
      let best = 0;
      let bestDist = Infinity;
      intervals.forEach((item, idx) => {
        const cx = (xAt(item.from) + xAt(item.to)) / 2;
        const dist = Math.abs(svgX - cx);
        if (dist < bestDist) {
          bestDist = dist;
          best = idx;
        }
      });
      setActiveIndex(best);
    } else {
      let best = 0;
      let bestDist = Infinity;
      points.forEach(([at], idx) => {
        const dist = Math.abs(svgX - xAt(at));
        if (dist < bestDist) {
          bestDist = dist;
          best = idx;
        }
      });
      setActiveIndex(best);
    }
  };

  const moveFocus = (next: number, total: number) => {
    const clamped = Math.max(0, Math.min(total - 1, next));
    setActiveIndex(clamped);
    itemRefs.current[clamped]?.focus();
  };

  const activeDelta =
    mode === "delta" && activeIndex !== null ? intervals[activeIndex] : undefined;
  const activePoint =
    mode === "total" && activeIndex !== null ? points[activeIndex] : undefined;
  const activePrevPoint =
    mode === "total" && activeIndex !== null && activeIndex > 0
      ? points[activeIndex - 1]
      : undefined;
  const activePointGap =
    activePoint && activePrevPoint
      ? trend.gaps.some(
          ([start, end]) =>
            start === activePrevPoint[0] && end === activePoint[0],
        )
      : false;

  return (
    <div className={`chart-content${refreshing ? " is-refreshing" : ""}`}>
      <div className="chart-detail" aria-live="polite">
        {activeDelta ? (
          <>
            <span>{activeDelta.date}</span>
            <strong>
              {activeDelta.duration}{" "}
              {activeDelta.delta === 0 ? "0" : formatSigned(activeDelta.delta)}
            </strong>
            {activeDelta.missing && (
              <span className="chart-detail-note">缺测</span>
            )}
          </>
        ) : activePoint ? (
          <>
            <span>{formatChartPointTime(activePoint[0])}</span>
            <strong>{formatNumber(activePoint[1])} Star</strong>
            {activePrevPoint && (
              <span
                className={
                  activePoint[1] - activePrevPoint[1] < 0
                    ? "chart-detail-delta negative"
                    : "chart-detail-delta"
                }
              >
                {activePoint[1] - activePrevPoint[1] === 0
                  ? "0"
                  : formatSigned(activePoint[1] - activePrevPoint[1])}
              </span>
            )}
            {activePointGap && <span className="chart-detail-note">含缺测</span>}
          </>
        ) : (
          <span className="chart-detail-hint">悬停或滑动查看快照明细</span>
        )}
      </div>

      <svg
        ref={svgRef}
        className="chart"
        viewBox={`0 0 ${scale.viewWidth} 128`}
        role="group"
        aria-label={
          mode === "delta"
            ? `相邻快照 Star 净增量柱状图，${trendStateLabel(trend)}，窗口净增量 ${trend.basis === "unknown" ? "暂不可知" : formatSigned(trend.delta)}，${trend.gaps.length} 段缺测`
            : `Star 总数折线图，${trendStateLabel(trend)}，共 ${points.length} 次观测，${trend.gaps.length} 段缺测`
        }
        onPointerDown={(event) => handleScrub(event.clientX)}
        onPointerMove={(event) => {
          if (event.pointerType !== "mouse" && event.buttons === 0) return;
          handleScrub(event.clientX);
        }}
        onPointerLeave={() => setActiveIndex(null)}
      >
        {scale.ticks.map((value, index) => (
          <g key={value}>
            <line
              className={mode === "delta" && value === 0 ? "chart-zero" : "chart-grid"}
              x1={PLOT_LEFT}
              x2={PLOT_RIGHT}
              y1={scale.y(value)}
              y2={scale.y(value)}
            />
            <text x="240" y={scale.y(value) + 3}>
              {scale.tickLabels[index]}
            </text>
          </g>
        ))}

        {mode === "delta" ? (
          intervals.map(({ from, to, delta, missing, date, duration }, index) => {
            if (from === to) return null;
            const x1 = xAt(from);
            const x2 = xAt(to);
            const width = Math.min(28, (x2 - x1) * 0.76);
            const isActive = activeIndex === index;
            return (
              <g key={`${from}-${to}`}>
                <rect
                  className={`chart-bar${delta < 0 ? " negative" : ""}${missing ? " missing" : ""}${isActive ? " active" : ""}`}
                  x={(x1 + x2 - width) / 2}
                  y={
                    delta === 0
                      ? deltaScale.zero - 0.5
                      : Math.min(deltaScale.zero, deltaScale.y(delta))
                  }
                  width={width}
                  height={
                    delta === 0
                      ? 1.5
                      : Math.abs(deltaScale.y(delta) - deltaScale.zero)
                  }
                />
                <rect
                  ref={(el) => {
                    itemRefs.current[index] = el;
                  }}
                  className="chart-hit"
                  x={x1}
                  y={PLOT_TOP - 4}
                  width={Math.max(2, x2 - x1)}
                  height={PLOT_HEIGHT + 8}
                  fill="transparent"
                  role="img"
                  tabIndex={0}
                  aria-label={`${date}，${duration}，${delta === 0 ? "0" : formatSigned(delta)} Star${missing ? "，缺测区间" : ""}`}
                  onFocus={() => setActiveIndex(index)}
                  onBlur={() => setActiveIndex(null)}
                  onKeyDown={(event) => {
                    if (event.key === "ArrowLeft") {
                      event.preventDefault();
                      moveFocus(index - 1, intervals.length);
                    } else if (event.key === "ArrowRight") {
                      event.preventDefault();
                      moveFocus(index + 1, intervals.length);
                    }
                  }}
                />
              </g>
            );
          })
        ) : (
          <>
            {points.slice(1).map(([to, stars], index) => {
              const [from, prevStars] = points[index];
              const missing = trend.gaps.some(
                ([start, end]) => start === from && end === to,
              );
              return (
                <line
                  key={`${from}-${to}`}
                  className={`chart-line${missing ? " missing" : ""}`}
                  x1={xAt(from)}
                  y1={totalScale.y(prevStars)}
                  x2={xAt(to)}
                  y2={totalScale.y(stars)}
                />
              );
            })}
            {activePoint && (
              <line
                className="chart-guide"
                x1={xAt(activePoint[0])}
                x2={xAt(activePoint[0])}
                y1={PLOT_TOP}
                y2={PLOT_BOTTOM}
              />
            )}
            {points.map(([at, stars], index) => {
              const isActive = activeIndex === index;
              const cx = xAt(at);
              const cy = totalScale.y(stars);
              return (
                <circle
                  key={at}
                  ref={(el) => {
                    itemRefs.current[index] = el;
                  }}
                  className={`chart-dot${isActive ? " active" : ""}`}
                  cx={cx}
                  cy={cy}
                  r={isActive ? 3.5 : 2}
                  role="img"
                  tabIndex={0}
                  aria-label={`${formatChartPointTime(at)}，${formatNumber(stars)} Star`}
                  onFocus={() => setActiveIndex(index)}
                  onBlur={() => setActiveIndex(null)}
                  onKeyDown={(event) => {
                    if (event.key === "ArrowLeft") {
                      event.preventDefault();
                      moveFocus(index - 1, points.length);
                    } else if (event.key === "ArrowRight") {
                      event.preventDefault();
                      moveFocus(index + 1, points.length);
                    }
                  }}
                />
              );
            })}
          </>
        )}

        <text x={PLOT_LEFT} y="116">
          {formatChartAxisDate(first)}
        </text>
        <text x={PLOT_RIGHT} y="116" textAnchor="end">
          最新快照
        </text>
      </svg>
    </div>
  );
}
