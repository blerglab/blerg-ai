-- V2 is deliberately separate from connection-bound v1 consent. A row is
-- metadata and a withdrawal tombstone; it cannot replace either process's
-- one-use affirmative memory or authorize recovery after a restart.
CREATE TABLE shell_host_consents_v2 (
 challenge_id text PRIMARY KEY,
 acceptance_id text NOT NULL UNIQUE,
 owner_id text NOT NULL,
 login_id text NOT NULL,
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 consent jsonb NOT NULL,
 expires_at timestamptz NOT NULL,
 accepted_at timestamptz,
 withdrawn_at timestamptz
);
CREATE INDEX shell_host_consents_v2_owner ON shell_host_consents_v2(session_id,owner_id,login_id);
