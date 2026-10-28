import { WINDOWS, PROJECT_TYPES, type ProjectType, type CleanupResult } from "./contracts";
import { projects, samplingPolicy, setState, state, type ProjectRow } from "./store";
import { DAY, HOUR, RETENTION } from "./trends";
import { isStale, type SamplingPolicy } from "./schedule";

export const PROJECT_TARGET = 500;

export async function prune(db: D1Database, now: number) {
  const cutoff = now - RETENTION;
  // Queries enforce the exact rolling cutoff; retain the boundary day intact.
  return db.prepare("DELETE FROM snapshot_days WHERE day < ?")
    .bind(Math.floor(cutoff / DAY)).run();
}
export async function cleanup(db: D1Database, now: number, policy?: SamplingPolicy) {
  // Most recent 04:00 Asia/Shanghai, including catch-up after a missed tick.
  const at = Math.floor((now + 4 * HOUR) / DAY) * DAY - 4 * HOUR;
  let changed = false;
  for (const type of PROJECT_TYPES) {
    const previous = await state<CleanupResult>(db, `cleanup:${type}`)
      ?? (type === "github" ? await state<CleanupResult>(db, "cleanup") : null);
    if (previous && previous.at >= at) continue;
    policy ??= await samplingPolicy(db);
    await cleanupType(db, type, now, at, policy);
    changed = true;
  }
  return changed;
}
async function cleanupType(db: D1Database, type: ProjectType, now: number, at: number, policy?: SamplingPolicy) {
  const rows = (await db.prepare("SELECT * FROM projects WHERE type=?").bind(type).all<ProjectRow>()).results;
  if (rows.length <= PROJECT_TARGET) {
    await setState(db, `cleanup:${type}`, {
      at,
      removed: 0,
      remaining: rows.length,
      eligible: 0,
    }).run();
    return true;
  }
  const potential = rows.filter(
    (row) => now - row.entered_at >= DAY && !isStale(row.observed_at, now, policy),
  );
  const summaries = await projects(db, potential, now, policy);
  const weights = [0.6, 0.25, 0.1, 0.05];
  const candidates = potential
    .flatMap((row, index) => {
      const trends = summaries[index].trends;
      const longest = trends[720];
      if (longest.start === null || longest.end === null || longest.end - longest.start < DAY)
        return [];
      if (WINDOWS.some((hours) => trends[hours].delta === null)) return [];
      const score = WINDOWS.reduce((sum, hours, i) => {
        const trend = trends[hours];
        return (
          sum +
          (Math.max(trend.delta!, 0) / ((trend.end! - trend.start!) / DAY)) *
            weights[i]
        );
      }, 0);
      return [{ row, score, delta: trends[24].delta! }];
    })
    .sort(
      (a, b) =>
        a.score - b.score ||
        a.row.listed_at - b.row.listed_at ||
        a.delta - b.delta ||
        a.row.id.localeCompare(b.row.id, "en"),
    );
  const victims = candidates.slice(0, Math.max(0, rows.length - PROJECT_TARGET));
  // Freeze candidate ids + cycles before committing; retries cannot touch a new cycle.
  const statements = victims.map(({ row }) =>
    db
      .prepare(
        "DELETE FROM projects WHERE id=? AND cycle=? AND entered_at <= ?",
      )
      .bind(row.id, row.cycle, now - DAY),
  );
  statements.push(
    setState(db, `cleanup:${type}`, {
      at,
      removed: victims.length,
      remaining: rows.length - victims.length,
      eligible: candidates.length,
    }),
  );
  await db.batch(statements);
  return true;
}
