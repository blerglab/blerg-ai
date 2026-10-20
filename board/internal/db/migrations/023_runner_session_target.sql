-- Where a person chose to run a card (runtime "cluster" or "docker", and the daemon), and the
-- automation account that start ran as. A respawn of the card's worker keeps the choice while the
-- board's automation still runs as that account. Empty: the board's default.
ALTER TABLE runner_sessions ADD COLUMN target_runtime text NOT NULL DEFAULT '';
ALTER TABLE runner_sessions ADD COLUMN target_daemon_id text NOT NULL DEFAULT '';
ALTER TABLE runner_sessions ADD COLUMN target_account text NOT NULL DEFAULT '';
