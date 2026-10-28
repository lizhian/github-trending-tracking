-- Metadata refreshes piggyback on observations; sampling uses this index to avoid
-- returning and parsing all project summaries on every minute tick.
ALTER TABLE projects ADD COLUMN metadata_at INTEGER NOT NULL DEFAULT 0;
CREATE INDEX projects_due ON projects(observed_at, attempted_at, id);
