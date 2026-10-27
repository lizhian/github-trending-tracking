import { WINDOWS, type CleanupResult } from "./contracts";
import { histories, setState, state, type ProjectRow } from "./store";
import { allTrends, DAY, HOUR, RETENTION } from "./trends";

export async function prune(db: D1Database, now: number) {
  const cutoff = now - RETENTION;
  // The boundary day is rewritten once per ten-minute tick; older days are removed.
  // JSON reconstruction drops individual expired hours, not an entire valid boundary day.
  await db.batch([
    db
      .prepare("DELETE FROM snapshot_days WHERE day < ?")
      .bind(Math.floor(cutoff / DAY)),
    db
      .prepare(
        `UPDATE snapshot_days SET hours=COALESCE((SELECT json_group_object(key,json(value)) FROM json_each(snapshot_days.hours) WHERE json_extract(value,'$[0]') >= ?),'{}') WHERE day=? AND EXISTS (SELECT 1 FROM json_each(snapshot_days.hours) WHERE json_extract(value,'$[0]') < ?)`,
      )
      .bind(cutoff, Math.floor(cutoff / DAY), cutoff),
  ]);
}
export async function cleanup(db: D1Database, now: number) {
  // Most recent 04:00 Asia/Shanghai, including catch-up after a missed tick.
  const at = Math.floor((now + 4 * HOUR) / DAY) * DAY - 4 * HOUR;
  const previous = await state<CleanupResult>(db, "cleanup");
  if (previous && previous.at >= at) return;
  const rows = (await db.prepare("SELECT * FROM projects").all<ProjectRow>())
    .results;
  if (rows.length <= 1000) {
    await setState(db, "cleanup", {
      at,
      removed: 0,
      remaining: rows.length,
      eligible: 0,
    }).run();
    return;
  }
  const potential = rows.filter(
    (row) => now - row.entered_at >= DAY && now - row.observed_at <= 2 * HOUR,
  );
  const history = await histories(
    db,
    potential.map((row) => row.cycle),
    now,
  );
  const weights = [0.6, 0.25, 0.1, 0.05];
  const candidates = potential
    .flatMap((row) => {
      const points = history.get(row.cycle) ?? [];
      if (points.length < 2 || points.at(-1)![0] - points[0][0] < DAY)
        return [];
      const trends = allTrends(points, now);
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
  const victims = candidates.slice(0, Math.max(0, rows.length - 1000));
  // Freeze candidate ids + cycles before committing; retries cannot touch a new cycle.
  const statements = victims.map(({ row }) =>
    db
      .prepare(
        "DELETE FROM projects WHERE id=? AND cycle=? AND entered_at <= ?",
      )
      .bind(row.id, row.cycle, now - DAY),
  );
  statements.push(
    setState(db, "cleanup", {
      at,
      removed: victims.length,
      remaining: rows.length - victims.length,
      eligible: candidates.length,
    }),
  );
  await db.batch(statements);
}
