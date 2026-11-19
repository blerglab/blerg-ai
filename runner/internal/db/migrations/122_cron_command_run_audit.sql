CREATE TABLE cron_command_run_audit (
 run_id uuid PRIMARY KEY,
 cron_id uuid NOT NULL,
 definition_hash text NOT NULL,
 definition jsonb NOT NULL,
 runtime text NOT NULL,
 daemon_id text NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT now()
);
