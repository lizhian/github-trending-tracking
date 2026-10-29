CREATE TABLE submissions (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL COLLATE NOCASE,
  repo_id TEXT,
  phase TEXT NOT NULL DEFAULT 'repository' CHECK(phase IN ('repository','linuxdo','done','alias')),
  github TEXT NOT NULL DEFAULT 'pending',
  linuxdo TEXT NOT NULL DEFAULT 'pending',
  repository_attempts INTEGER NOT NULL DEFAULT 0,
  readme_attempts INTEGER NOT NULL DEFAULT 0,
  next_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  finished_at INTEGER,
  message TEXT NOT NULL DEFAULT '',
  merged_into TEXT REFERENCES submissions(id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX submissions_active_name ON submissions(name) WHERE phase IN ('repository','linuxdo');
CREATE UNIQUE INDEX submissions_active_repo ON submissions(repo_id) WHERE repo_id IS NOT NULL AND phase IN ('repository','linuxdo');
CREATE INDEX submissions_due ON submissions(next_at,created_at) WHERE phase IN ('repository','linuxdo');
