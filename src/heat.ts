import { WINDOWS, type Project, type WindowHours } from "../shared/contracts.ts";

const DAY = 86_400_000;

const HEAT_WEIGHTS: Record<WindowHours, number> = {
  24: 0.6,
  72: 0.25,
  168: 0.1,
  720: 0.05,
};

type HeatWindowStatus = "scored" | "missing" | "stale" | "unknown";

export interface HeatWindowBreakdown {
  hours: WindowHours;
  weight: number;
  status: HeatWindowStatus;
  delta: number | null;
  actualDays: number | null;
  effectiveDays: number | null;
  contribution: number;
}

export interface HeatBreakdown {
  total: number;
  windows: HeatWindowBreakdown[];
}

export function projectHeatBreakdown(project: Project): HeatBreakdown {
  let total = 0;
  const windows: HeatWindowBreakdown[] = WINDOWS.map((hours) => {
    const trend = project.trends[hours];
    const weight = HEAT_WEIGHTS[hours];

    if (
      trend.basis === "unknown" ||
      (project.stale && project.unavailableAt === 0)
    ) {
      return {
        hours,
        weight,
        status: project.stale ? "stale" : "unknown",
        delta: null,
        actualDays: null,
        effectiveDays: null,
        contribution: 0,
      };
    }

    if (
      trend.delta === null ||
      trend.start === null ||
      trend.end === null ||
      trend.end <= trend.start
    ) {
      return {
        hours,
        weight,
        status: "missing",
        delta: null,
        actualDays: null,
        effectiveDays: null,
        contribution: 0,
      };
    }

    const actualDays = (trend.end - trend.start) / DAY;
    const effectiveDays = Math.max(actualDays, 1);
    const contribution = (Math.max(trend.delta, 0) / effectiveDays) * weight;
    total += contribution;

    return {
      hours,
      weight,
      status: "scored",
      delta: trend.delta,
      actualDays,
      effectiveDays,
      contribution,
    };
  });

  return { total, windows };
}

export function projectHeat(project: Project): number {
  return projectHeatBreakdown(project).total;
}
