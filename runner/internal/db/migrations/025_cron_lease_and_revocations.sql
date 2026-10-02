-- Cron follow-ups.
--
-- cron_runs.lease_until: a start in progress on the HTTP goroutine (run now) holds its run for a
-- while so the scheduler's reaper does not re-drive a start that is merely slow. NULL for every
-- scheduler-claimed run, which the tick's own single-flight already protects.
ALTER TABLE cron_runs ADD COLUMN lease_until timestamptz;

-- cron_token_revocations: cron tokens whose revocation at core is owed. A pause, a renewal or a
-- resume enqueues the token it retires here first, tries core, and removes the row on core's
-- acknowledgement; a leader retries what is left every watchdog pass, so a core that was down at
-- pause time cannot leave a paused cron's token live (spec 7.6).
CREATE TABLE cron_token_revocations (
  token_id   text PRIMARY KEY,
  account_id text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
