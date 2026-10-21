-- A board's default execution target is chosen by its automation owner.
ALTER TABLE boards ADD COLUMN run_runtime text NOT NULL DEFAULT '' CHECK (run_runtime IN ('', 'cluster', 'docker', 'daemon'));
ALTER TABLE boards ADD COLUMN run_daemon_id text NOT NULL DEFAULT '';
ALTER TABLE boards ADD COLUMN run_target_account text NOT NULL DEFAULT '';
ALTER TABLE boards ADD CONSTRAINT board_run_target_pair CHECK (
 (run_runtime IN ('', 'cluster') AND run_daemon_id = '') OR
 (run_runtime IN ('docker', 'daemon') AND run_daemon_id <> '')
);
