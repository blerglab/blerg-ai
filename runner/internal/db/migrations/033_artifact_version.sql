-- File versions: publishing a file whose name already exists in the session, from the same origin,
-- makes the next version of it. The number is STORED (deleting v1 never renumbers v2); the insert
-- takes MAX(version) + 1 per (session_id, origin, name) under the per-session artifacts lock.
ALTER TABLE session_artifacts ADD COLUMN IF NOT EXISTS version int NOT NULL DEFAULT 1;

-- Number the files that already exist, oldest first, per (session, origin, name). Only rows still at
-- the default 1 that are not the oldest of their name are touched, so running this again (or after a
-- version was deleted) changes nothing.
UPDATE session_artifacts a
   SET version = r.rn
  FROM (SELECT id, row_number() OVER (PARTITION BY session_id, origin, name ORDER BY created_at, id) AS rn
          FROM session_artifacts) r
 WHERE a.id = r.id AND a.version = 1 AND r.rn <> 1;
