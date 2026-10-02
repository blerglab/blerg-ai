-- 023: which cron started a session (spec 7.5, 7.6).
--
-- cron_id is set by the scheduler's start path on every session a cron starts. It drives the
-- "cron" badge in the UI, the per-account concurrent cron session limit, the max-runtime
-- watchdog and the stop-on-delete/pause of a cron's sessions. It is deliberately NOT a
-- foreign key: deleting a cron leaves its finished sessions (and their transcripts) behind.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS cron_id uuid;
CREATE INDEX IF NOT EXISTS sessions_cron_id ON sessions (cron_id) WHERE cron_id IS NOT NULL;
