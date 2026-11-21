ALTER TABLE crons ADD COLUMN command_steps jsonb;
CREATE TABLE cron_command_step_audit (
 id bigserial PRIMARY KEY,
 cron_id uuid NOT NULL,
 owner_account_id text NOT NULL,
 definition jsonb NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX cron_command_step_audit_cron ON cron_command_step_audit(cron_id, id);
