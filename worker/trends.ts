import { WINDOWS, type Point, type Trend, type WindowHours } from "./contracts";
export const HOUR = 3_600_000;
export const DAY = 24 * HOUR;
export const RETENTION = 30 * DAY;

export function windowTrend(
  points: Point[],
  hours: WindowHours,
  now: number,
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
    gaps: selected
      .slice(1)
      .flatMap((point, i) =>
        Math.floor(point[0] / HOUR) - Math.floor(selected[i][0] / HOUR) > 1
          ? [[selected[i][0], point[0]] as [number, number]]
          : [],
      ),
  };
}

// Keep extrema/endpoints and every gap boundary, so miniatures never bridge missing samples.
function compact(points: Point[]): Point[] {
  if (points.length <= 48) return points;
  const keep = new Set([0, 1, 2, 3, points.length - 1]);
  const width = Math.ceil(points.length / 20);
  for (let start = 0; start < points.length; start += width) {
    const end = Math.min(start + width, points.length);
    let min = start,
      max = start;
    for (let i = start; i < end; i++) {
      if (points[i][1] < points[min][1]) min = i;
      if (points[i][1] > points[max][1]) max = i;
    }
    keep.add(min);
    keep.add(max);
  }
  for (let i = 1; i < points.length; i++)
    if (
      Math.floor(points[i][0] / HOUR) - Math.floor(points[i - 1][0] / HOUR) >
      1
    ) {
      keep.add(i - 1);
      keep.add(i);
    }
  return [...keep].sort((a, b) => a - b).map((i) => points[i]);
}
export function allTrends(points: Point[], now: number) {
  return Object.fromEntries(
    WINDOWS.map((hours) => {
      const trend = windowTrend(points, hours, now);
      return [hours, { ...trend, points: compact(trend.points) }];
    }),
  ) as Record<WindowHours, Trend>;
}
