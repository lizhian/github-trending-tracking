import { type Project, type Point } from "./contracts";
import { allTrends, DAY, HOUR, RETENTION } from "./trends";
import { type Repository } from "./github";
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
export function project(row: ProjectRow, now: number): Project {
  let trends: Project["trends"] = JSON.parse(row.trends);
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
    stale: now - row.observed_at > 2 * HOUR,
    trends,
  };
}
export async function saveObservations(
  db: D1Database,
  observations: { repo: Repository; previous?: ProjectRow; listed: boolean }[],
  now: number,
) {
  const history = await histories(
    db,
    observations.flatMap((o) => (o.previous ? [o.previous.cycle] : [])),
    now,
  );
  const statements: D1PreparedStatement[] = [];
  for (const { repo, previous, listed } of observations) {
    const cycle = previous?.cycle ?? crypto.randomUUID();
    const hour = Math.floor(now / HOUR);
    const points = (history.get(cycle) ?? []).filter(
      ([time]) => Math.floor(time / HOUR) !== hour,
    );
    points.push([now, repo.stargazerCount]);
    points.sort((a, b) => a[0] - b[0]);
    const values = [
      repo.nameWithOwner,
      repo.description ?? null,
      repo.primaryLanguage?.name ?? null,
      JSON.stringify(repo.repositoryTopics.nodes.map((n) => n.topic.name)),
      now,
      now,
      repo.stargazerCount,
      JSON.stringify(allTrends(points, now)),
    ];
    if (previous) {
      statements.push(
        db
          .prepare(
            `UPDATE projects SET name=?,description=?,language=?,topics=?,observed_at=?,attempted_at=?,stars=?,trends=?,error=NULL,listed_at=MAX(listed_at,?) WHERE id=? AND cycle=? AND observed_at < ?`,
          )
          .bind(
            ...values,
            listed ? now : previous.listed_at,
            repo.id,
            cycle,
            now,
          ),
      );
    } else {
      statements.push(
        db
          .prepare(
            `INSERT INTO projects(id,cycle,name,description,language,topics,observed_at,attempted_at,stars,trends,entered_at,listed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
          )
          .bind(repo.id, cycle, ...values, now, now),
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
