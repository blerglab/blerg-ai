-- Round-2 cron fixes.
--
-- cron_runs.redrives: how many times the scheduler's reaper has re-driven a claimed run whose start
-- never finished. A start that always times out is re-driven a few times, then the run fails,
-- instead of being retried every minute for up to 23 hours.
ALTER TABLE cron_runs ADD COLUMN redrives int NOT NULL DEFAULT 0;

-- cron_token_revocations.attempts / next_attempt_at: a revocation core keeps refusing backs off
-- (30 s doubling to an hour) and is dropped after 20 attempts, so one poisoned row cannot sit at the
-- head of the retry queue and starve the ones behind it.
ALTER TABLE cron_token_revocations
  ADD COLUMN attempts int NOT NULL DEFAULT 0,
  ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT 'epoch';
