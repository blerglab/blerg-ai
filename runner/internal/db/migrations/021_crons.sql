-- Crons (spec 7.1): a schedule plus prompt, target and MCP grant that starts an
-- ordinary one-shot agent session on a schedule, and one row per firing.
CREATE TABLE crons (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_account_id     text NOT NULL,
  name                 text NOT NULL,
  enabled              boolean NOT NULL DEFAULT true,
  schedule             text NOT NULL,
  timezone             text NOT NULL,
  prompt               text NOT NULL CHECK (length(prompt) <= 16384),
  engine               text NOT NULL DEFAULT 'claude' CHECK (engine = 'claude'),
  model                text,
  effort               text,
  runtime              text NOT NULL DEFAULT 'auto' CHECK (runtime IN ('auto','cluster','docker')),
  daemon_id            uuid,                       -- optional pin for the docker runtime (honoured in process only)
  board_id             text,                       -- target board (spec section 10)
  mcp                  jsonb NOT NULL DEFAULT '[]',
  token_id             text NOT NULL,
  token_expires_at     timestamptz NOT NULL,
  grace_seconds        int NOT NULL DEFAULT 3600,
  max_runtime_seconds  int NOT NULL DEFAULT 1800,
  next_run_at          timestamptz NOT NULL,
  last_run_at          timestamptz,
  consecutive_failures int NOT NULL DEFAULT 0,
  paused_reason        text,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX crons_owner ON crons (owner_account_id);
CREATE INDEX crons_due ON crons (next_run_at) WHERE enabled AND paused_reason IS NULL;

CREATE TABLE cron_runs (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  cron_id       uuid NOT NULL REFERENCES crons(id) ON DELETE CASCADE,
  scheduled_for timestamptz NOT NULL,
  claimed_at    timestamptz NOT NULL DEFAULT now(),
  started_at    timestamptz,
  session_id    uuid,
  status        text NOT NULL CHECK (status IN ('claimed','started','held','skipped','failed')),
  reason        text,
  late          boolean NOT NULL DEFAULT false,
  manual        boolean NOT NULL DEFAULT false
);
CREATE INDEX cron_runs_open ON cron_runs (status) WHERE status IN ('claimed','held');
CREATE INDEX cron_runs_by_cron ON cron_runs (cron_id, scheduled_for DESC);
CREATE INDEX cron_runs_claimed_at ON cron_runs (claimed_at);
