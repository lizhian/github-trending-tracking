import { WINDOWS, type Project } from "../shared/contracts.ts";

const DAY = 86_400_000;
const weights = [0.6, 0.25, 0.1, 0.05];

export function projectHeat(project: Project): number | null {
  const longest = project.trends[720];
  if (
    longest.start === null || longest.end === null ||
    longest.end - longest.start < DAY ||
    (project.unavailableAt > 0 && project.observedAt - project.enteredAt < DAY)
  ) return null;

  let score = 0;
  for (const [index, hours] of WINDOWS.entries()) {
    const trend = project.trends[hours];
    if (
      trend.basis === "unknown" || trend.delta === null ||
      trend.start === null || trend.end === null || trend.end <= trend.start
    ) return null;
    score += Math.max(trend.delta, 0) / ((trend.end - trend.start) / DAY) * weights[index];
  }
  return score;
}
