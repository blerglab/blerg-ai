-- Inert until human approval and the live scheduler are reviewed.
CREATE TABLE live_mcp_binding_state (
 binding_id uuid PRIMARY KEY,
 revision bigint NOT NULL CHECK (revision>0),
 scope jsonb NOT NULL,
 enabled boolean NOT NULL DEFAULT false,
 interval_seconds integer NOT NULL DEFAULT 60 CHECK(interval_seconds>=15),
 fence bigint NOT NULL CHECK (fence>0),
 lease_until timestamptz NOT NULL,
 demand_until timestamptz NOT NULL
);
CREATE TABLE live_mcp_attempts (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 binding_id uuid NOT NULL,
 revision bigint NOT NULL CHECK (revision>0),
 owner_id uuid NOT NULL,
 connection_id uuid NOT NULL,
 scope jsonb NOT NULL,
 fence bigint NOT NULL CHECK (fence>0),
 due_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 dispatched_at timestamptz,
 finished_at timestamptz,
 outcome text NOT NULL DEFAULT 'undispatched' CHECK (outcome IN ('undispatched','dispatched','ok','failed','interrupted')),
 row_count integer NOT NULL DEFAULT 0 CHECK (row_count>=0 AND row_count<=10000),
 CHECK (expires_at>due_at),
 CHECK ((outcome='undispatched')=(dispatched_at IS NULL)),
 CHECK (finished_at IS NULL OR dispatched_at IS NOT NULL)
);
CREATE INDEX live_mcp_attempt_owner ON live_mcp_attempts(owner_id,dispatched_at);
CREATE INDEX live_mcp_attempt_connection ON live_mcp_attempts(connection_id,dispatched_at);
CREATE INDEX live_mcp_attempt_retention ON live_mcp_attempts(created_at);
CREATE TABLE live_mcp_authority_epochs (
 installation_id uuid NOT NULL,
 runner_id uuid NOT NULL,
 epoch bigint NOT NULL CHECK(epoch>0),
 PRIMARY KEY(installation_id,runner_id)
);
