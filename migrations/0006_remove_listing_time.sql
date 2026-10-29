-- Drop in place so project cycles and snapshot foreign keys remain intact.
ALTER TABLE projects DROP COLUMN listed_at;

UPDATE system_state
SET value = (SELECT json_group_array(json_remove(entry.value, '$.listedAt'))
             FROM json_each(system_state.value) AS entry)
WHERE key = 'pending' AND json_type(value) = 'array';

UPDATE system_state
SET value = json_set(value, '$.details', json((
  SELECT json_group_array(json_remove(task.value, '$.listedAt'))
  FROM json_each(system_state.value, '$.details') AS task
)))
WHERE key = 'discoverySources' AND json_type(value, '$.details') = 'array';
