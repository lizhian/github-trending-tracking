import { WINDOWS, type Point, type Trend, type TrendSummary, type WindowHours } from "./contracts";
export const HOUR = 3_600_000;
export const DAY = 24 * HOUR;
export const RETENTION = 30 * DAY;

export function windowTrend(
  points: Point[],
  hours: WindowHours,
  now: number,
  observedGaps?: Trend["gaps"],
): Trend {
  const valid = points.filter(
    ([time]) => time >= now - RETENTION && time <= now,
  );
  const end = valid.at(-1)?.[0] ?? null;
  const selected =
    end === null ? [] : valid.filter(([time]) => time >= end - hours * HOUR);
  const first = selected[0];
  const last = selected.at(-1);
  const comparable = first && last && last[0] > first[0];
  return {
    delta: comparable ? last[1] - first[1] : null,
    state: !comparable
      ? "accumulating"
      : last[0] - first[0] >= (hours - 1) * HOUR
        ? "complete"
        : "partial",
    start: first?.[0] ?? null,
    end: last?.[0] ?? null,
    points: selected,
    gaps: observedGaps
      ? observedGaps.filter(
          ([from, to]) =>
            from >= (first?.[0] ?? now) && to <= (last?.[0] ?? now),
        )
      : selected
          .slice(1)
          .flatMap((point, i) =>
            Math.floor(point[0] / HOUR) - Math.floor(selected[i][0] / HOUR) > 1
              ? [[selected[i][0], point[0]] as [number, number]]
              : [],
          ),
  };
}

// Baselines are exact observations, selected by D1 without returning full histories.
export function summarize(
  baselines: Partial<Record<WindowHours, Point>>,
  latest: Point | null,
  gaps: Trend["gaps"],
) {
  return Object.fromEntries(
    WINDOWS.map((hours) => {
      const first = baselines[hours] ?? latest;
      const start = first?.[0] ?? null;
      const end = latest?.[0] ?? null;
      const comparable = first && latest && latest[0] > first[0];
      const trend: TrendSummary = {
        delta: comparable ? latest[1] - first[1] : null,
        state: !comparable ? "accumulating"
          : latest[0] - first[0] >= (hours - 1) * HOUR ? "complete" : "partial",
        start, end,
        gaps: gaps.filter(([from, to]) => from >= (start ?? Infinity) && to <= (end ?? -Infinity)),
      };
      return [hours, trend];
    }),
  ) as Record<WindowHours, TrendSummary>;
}
