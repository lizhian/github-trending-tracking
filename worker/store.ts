import { WINDOWS, type Project, type Point, type WindowHours } from "./contracts";
import { summarize, DAY, HOUR, RETENTION } from "./trends";
import { type Repository, type RepositoryStars } from "./github";
import { completeWindow, isStale, missedSampling, type SamplingPolicy } from "./schedule";
export interface ProjectRow {
  id: string;
  cycle: string;
  name: string;
  description: string | null;
  language: string | null;
  topics: string;
  stars: number;
  entered_at: number;
  listed_at: number;
  observed_at: number;
  attempted_at: number;
  error: string | null;
  trends: string;
  metadata_at: number;
}
export async function state<T>(db: D1Database, key: string): Promise<T | null> {
  const row = await db
    .prepare("SELECT value FROM system_state WHERE key = ?")
    .bind(key)
    .first<{ value: string }>();
  return row ? JSON.parse(row.value) : null;
}
export function setState(db: D1Database, key: string, value: unknown) {
  return db
    .prepare(
      "INSERT INTO system_state(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
    )
    .bind(key, JSON.stringify(value));
}
export async function samplingPolicy(db: D1Database, initializeAt?: number): Promise<SamplingPolicy | undefined> {
  const prior = await state<SamplingPolicy>(db, "sampling_policy");
  if (prior || initializeAt === undefined) return prior ?? undefined;
  // The scheduler calls this under its lease; readers never initialize policy.
  const policy: SamplingPolicy = { version: 2, effectiveAt: initializeAt };
  await setState(db, "sampling_policy", policy).run();
  return policy;
}
export async function histories(
  db: D1Database,
  cycles: string[],
  now: number,
): Promise<Map<string, Point[]>> {
  const output = new Map<string, Point[]>();
  for (let start = 0; start < cycles.length; start += 90) {
    const group = cycles.slice(start, start + 90);
    const { results } = await db
      .prepare(
        `SELECT cycle,hours FROM snapshot_days WHERE cycle IN (${group.map(() => "?").join(",")}) AND day >= ? ORDER BY day`,
      )
      .bind(...group, Math.floor((now - RETENTION) / DAY))
      .all<{ cycle: string; hours: string }>();
    for (const row of results) {
      const points = Object.values(
        JSON.parse(row.hours) as Record<string, Point>,
      ).filter(([time]) => time >= now - RETENTION && time <= now);
      output.set(row.cycle, [...(output.get(row.cycle) ?? []), ...points]);
    }
  }
  for (const points of output.values()) points.sort((a, b) => a[0] - b[0]);
  return output;
}
/** Read only the first eligible day for each window, then at most its 24 points.
 * Excluding the replacement hour makes same-hour observations newer-only.
 */
export async function baselines(
  db: D1Database,
  targets: { cycle: string; end: number }[],
  now: number,
  replacing = false,
  windows: readonly WindowHours[] = WINDOWS,
) {
  const output = new Map<string, Partial<Record<WindowHours, Point>>>();
  // Hourly data normally has the boundary hour or its next hour. Probe those
  // JSON keys directly; use the day search only for real missing observations.
  const atHour = (offset: number) => {
    const hour = `CAST((cutoff+${offset * HOUR})/${HOUR} AS INTEGER)`;
    const path = `'$."' || ${hour} || '"'`;
    return `(SELECT json_extract(hours,${path}) FROM snapshot_days
      WHERE cycle=bounds.cycle AND day=CAST((cutoff+${offset * HOUR})/${DAY} AS INTEGER)
        AND json_extract(hours,${path} || '[0]') BETWEEN cutoff AND end_at
        ${replacing ? `AND ${hour} != CAST(end_at/${HOUR} AS INTEGER)` : ""})`;
  };
  for (let offset = 0; offset < targets.length; offset += 45) {
    const group = targets.slice(offset, offset + 45);
    const result = await db.prepare(`
      WITH targets(cycle,end_at) AS (VALUES ${group.map(() => "(?,?)").join(",")}),
      windows(width) AS (VALUES ${windows.map((hours) => `(${hours})`).join(",")}),
      bounds AS (
        SELECT cycle,end_at,width,MAX(?,end_at-width*${HOUR}) AS cutoff
        FROM targets CROSS JOIN windows
      )
      SELECT cycle,width,COALESCE(${atHour(0)},${atHour(1)},(
        SELECT value FROM json_each((
          SELECT hours FROM snapshot_days AS days
          WHERE days.cycle=bounds.cycle
            AND day BETWEEN CAST(cutoff/${DAY} AS INTEGER) AND CAST(end_at/${DAY} AS INTEGER)
            AND EXISTS (
              SELECT 1 FROM json_each(hours)
              WHERE json_extract(value,'$[0]') BETWEEN cutoff AND end_at
                ${replacing ? `AND CAST(key AS INTEGER) != CAST(end_at/${HOUR} AS INTEGER)` : ""}
            )
          ORDER BY day LIMIT 1
        ))
        WHERE json_extract(value,'$[0]') BETWEEN cutoff AND end_at
          ${replacing ? `AND CAST(key AS INTEGER) != CAST(end_at/${HOUR} AS INTEGER)` : ""}
        ORDER BY CAST(key AS INTEGER) LIMIT 1
      )) AS point FROM bounds
    `).bind(...group.flatMap(({ cycle, end }) => [cycle, end]), now - RETENTION)
      .all<{ cycle: string; width: WindowHours; point: string | null }>();
    for (const { cycle, width, point } of result.results) {
      if (!output.has(cycle)) output.set(cycle, {});
      if (point) output.get(cycle)![width] = JSON.parse(point) as Point;
    }
  }
  return output;
}

export function project(row: ProjectRow, now: number, policy?: SamplingPolicy): Project {
  // Strip unused legacy graph points while existing rows refresh naturally.
  const stored: Project["trends"] = JSON.parse(row.trends);
  const trends = Object.fromEntries(Object.entries(stored).map(([hours, trend]) => [hours, {
    delta: trend.delta,
    state: trend.delta === null ? "accumulating"
      : completeWindow(trend.start!, trend.end!, Number(hours), policy) ? "complete" : "partial",
    start: trend.start, end: trend.end, gaps: trend.gaps,
  }])) as Project["trends"];
  return {
    id: row.id,
    name: row.name,
    description: row.description,
    language: row.language,
    topics: JSON.parse(row.topics),
    stars: row.stars,
    enteredAt: row.entered_at,
    listedAt: row.listed_at,
    observedAt: row.observed_at,
    attemptedAt: row.attempted_at,
    error: row.error,
    stale: isStale(row.observed_at, now, policy),
    trends,
  };
}

export async function projects(db: D1Database, rows: ProjectRow[], now: number, policy?: SamplingPolicy) {
  policy ??= await samplingPolicy(db);
  const items = rows.map((row) => project(row, now, policy));
  const expired = items.flatMap((item, index) =>
    Object.values(item.trends).some((trend) => trend.start !== null && trend.start < now - RETENTION)
      ? [{ item, row: rows[index] }] : [],
  );
  const widths = WINDOWS.filter((hours) => expired.some(({ item }) =>
    item.trends[hours].start !== null && item.trends[hours].start! < now - RETENTION));
  const actual = await baselines(db, expired.map(({ item, row }) => ({ cycle: row.cycle, end: item.observedAt })), now, false, widths);
  for (const { item, row } of expired) {
    const updated = summarize(actual.get(row.cycle) ?? {},
      item.observedAt >= now - RETENTION ? [item.observedAt, item.stars] : null,
      item.trends[720].gaps, policy);
    for (const hours of widths) {
      const start = item.trends[hours].start;
      if (start !== null && start < now - RETENTION) item.trends[hours] = updated[hours];
    }
  }
  return items;
}
export async function saveObservations(
  db: D1Database,
  observations: {
    repo: Repository | RepositoryStars;
    previous?: ProjectRow;
    listedAt: number | null;
  }[],
  now: number,
  policy?: SamplingPolicy,
) {
  policy ??= await samplingPolicy(db);
  const firstPoints = await baselines(
    db,
    observations.flatMap((o) => o.previous ? [{ cycle: o.previous.cycle, end: now }] : []),
    now,
    true,
  );
  const statements: D1PreparedStatement[] = [];
  for (const { repo, previous, listedAt } of observations) {
    const cycle = previous?.cycle ?? crypto.randomUUID();
    const hour = Math.floor(now / HOUR);
    const gaps: [number, number][] = previous ? JSON.parse(previous.trends)[720].gaps : [];
    if (previous && previous.observed_at < now) {
      if (Math.floor(previous.observed_at / HOUR) === hour) {
        const lastGap = gaps.at(-1);
        if (lastGap?.[1] === previous.observed_at) lastGap[1] = now;
      } else if (missedSampling(previous.observed_at, now, policy)) {
        gaps.push([previous.observed_at, now]);
      }
    }
    const metadata = "nameWithOwner" in repo;
    if (!previous && !metadata) throw new Error("New repository requires metadata");
    const values = [
      metadata ? repo.nameWithOwner : previous!.name,
      metadata ? repo.description ?? null : previous!.description,
      metadata ? repo.primaryLanguage?.name ?? null : previous!.language,
      metadata ? JSON.stringify(repo.repositoryTopics.nodes.map((n) => n.topic.name)) : previous!.topics,
      now,
      now,
      repo.stargazerCount,
      JSON.stringify(summarize(firstPoints.get(cycle) ?? {}, [now, repo.stargazerCount], gaps, policy)),
    ];
    if (previous) {
      statements.push(
        db
          .prepare(
            `UPDATE projects SET name=?,description=?,language=?,topics=?,observed_at=?,attempted_at=?,stars=?,trends=?,error=NULL,metadata_at=?,listed_at=MAX(listed_at,?) WHERE id=? AND cycle=? AND observed_at < ?`,
          )
          .bind(...values, metadata ? now : previous.metadata_at, listedAt ?? previous.listed_at, repo.id, cycle, now),
      );
    } else {
      statements.push(
        db
          .prepare(
            `INSERT INTO projects(id,cycle,name,description,language,topics,observed_at,attempted_at,stars,trends,entered_at,listed_at,metadata_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
          )
          .bind(repo.id, cycle, ...values, now, listedAt ?? now, now),
      );
    }
    const path = `$."${hour}"`;
    statements.push(
      db
        .prepare(
          `INSERT INTO snapshot_days(cycle,day,hours) SELECT ?,?,json_object(?,json(?)) FROM projects WHERE id=? AND cycle=? AND observed_at=?
      ON CONFLICT(cycle,day) DO UPDATE SET hours=json_set(snapshot_days.hours,?,json(?))
      WHERE COALESCE(json_extract(snapshot_days.hours,? || '[0]'),0) < ?`,
        )
        .bind(
          cycle,
          Math.floor(now / DAY),
          String(hour),
          JSON.stringify([now, repo.stargazerCount]),
          repo.id,
          cycle,
          now,
          path,
          JSON.stringify([now, repo.stargazerCount]),
          path,
          now,
        ),
    );
  }
  if (statements.length) await db.batch(statements);
}
