export const WINDOWS = [24, 72, 168, 720] as const;
export type WindowHours = (typeof WINDOWS)[number];
export type Point = [time: number, stars: number];
export const PROJECT_TYPES = ["github", "linux.do"] as const;
export type ProjectType = (typeof PROJECT_TYPES)[number];
export const projectId = (type: ProjectType, repoId: string) => `${type}:${repoId}`;
export interface TrendSummary {
  delta: number | null;
  state: "accumulating" | "partial" | "complete";
  start: number | null;
  end: number | null;
  gaps: [number, number][];
}
export interface Trend extends TrendSummary {
  points: Point[];
}
export interface Project {
  id: string;
  type: ProjectType;
  repoId: string;
  name: string;
  description: string | null;
  homepage: string | null;
  language: string | null;
  topics: string[];
  stars: number;
  enteredAt: number;
  observedAt: number;
  attemptedAt: number;
  error: string | null;
  stale: boolean;
  trends: Record<WindowHours, TrendSummary>;
}
export interface RunState {
  attemptedAt: number;
  succeededAt: number | null;
  succeeded: number;
  failed: number;
  error: string | null;
}
export interface ListResult {
  items: Project[];
  total: number;
  page: number;
  pageSize: number;
  pages: number;
  languages: string[];
  topics: string[];
  summary: {
    tracked: number;
    fresh: number;
    accumulating: number;
    net24: number | null;
    comparable24: number;
  };
  status: Record<string, RunState | CleanupResult>;
  asOf: number;
}
export interface HistoryResult {
  points: Point[];
  trend: Trend;
  gaps: [number, number][];
  observedAt: number;
  stale: boolean;
}
export interface CleanupResult {
  at: number;
  removed: number;
  remaining: number;
  eligible: number;
}

export interface Submission {
  id: string;
  name: string;
  url: string;
  status: "queued" | "processing" | "retrying" | "completed" | "partial" | "failed";
  github: "pending" | "tracked" | "failed";
  linuxdo: "pending" | "tracked" | "ineligible" | "unconfirmed" | "skipped";
  nextAttemptAt: number | null;
  expiresAt: number | null;
  message: string;
}
