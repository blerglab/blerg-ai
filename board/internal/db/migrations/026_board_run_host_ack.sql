-- A board that runs on a desktop's host ("This machine") runs card writers' prompts unattended,
-- without a sandbox, as the person who runs that desktop. The automation owner must acknowledge
-- that when saving the target; who acknowledged and when are kept with it. A host target saved
-- before this existed (unreleased) has no acknowledgement and is cleared.
ALTER TABLE boards ADD COLUMN IF NOT EXISTS run_host_ack_by text NOT NULL DEFAULT '';
ALTER TABLE boards ADD COLUMN IF NOT EXISTS run_host_ack_at timestamptz;
UPDATE boards SET run_runtime = '', run_daemon_id = '', run_target_account = '' WHERE run_runtime = 'daemon';
ALTER TABLE boards ADD CONSTRAINT board_run_host_ack CHECK (
 run_runtime <> 'daemon' OR (run_host_ack_by <> '' AND run_host_ack_by = run_target_account AND run_host_ack_at IS NOT NULL)
);
