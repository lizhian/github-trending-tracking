-- One mutable summary per stable GitHub id; a new UUID fences each tracking cycle.
CREATE TABLE projects (
  id TEXT PRIMARY KEY,
  cycle TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  description TEXT,
  language TEXT,
  topics TEXT NOT NULL,
  entered_at INTEGER NOT NULL,
  listed_at INTEGER NOT NULL,
  observed_at INTEGER NOT NULL,
  attempted_at INTEGER NOT NULL,
  error TEXT,
  stars INTEGER NOT NULL CHECK(stars >= 0),
  trends TEXT NOT NULL
);
-- JSON hour keys permit atomic, newer-only replacement without hourly index writes.
CREATE TABLE snapshot_days (
  cycle TEXT NOT NULL REFERENCES projects(cycle) ON DELETE CASCADE,
  day INTEGER NOT NULL,
  hours TEXT NOT NULL,
  PRIMARY KEY (cycle, day)
) WITHOUT ROWID;
CREATE INDEX snapshot_days_expiry ON snapshot_days(day);
CREATE TABLE system_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
