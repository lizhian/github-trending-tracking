-- Keep cycle unchanged: snapshot_days references cycle, so this identity migration
-- neither rebuilds projects nor invokes cascading deletion of existing history.
ALTER TABLE projects ADD COLUMN type TEXT NOT NULL DEFAULT 'github' CHECK(type IN ('github','linux.do'));
ALTER TABLE projects ADD COLUMN repo_id TEXT NOT NULL DEFAULT '';
UPDATE projects SET repo_id=id, id='github:' || id;
CREATE UNIQUE INDEX projects_type_repo ON projects(type,repo_id);
CREATE INDEX projects_repo ON projects(repo_id);
