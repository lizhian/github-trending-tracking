export const WINDOWS = [24, 72, 168, 720] as const;
export type WindowHours = (typeof WINDOWS)[number];
export type Point = [time: number, stars: number];
export interface Trend {
  delta: number | null;
  state: "accumulating" | "partial" | "complete";
  start: number | null;
  end: number | null;
  points: Point[];
  gaps: [number, number][];
}
export interface Project {
  id: string;
  name: string;
  description: string | null;
  language: string | null;
  topics: string[];
  stars: number;
  enteredAt: number;
  listedAt: number;
  observedAt: number;
  attemptedAt: number;
  error: string | null;
  stale: boolean;
  trends: Record<WindowHours, Trend>;
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
  summary: { tracked: number; fresh: number; accumulating: number };
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
