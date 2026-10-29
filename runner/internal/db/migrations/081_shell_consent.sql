-- A challenge is authority only after fresh owner affirmation. Every lookup is
-- bound to physical runtime, authenticated connection and actual OS identity.
CREATE TABLE shell_host_consents (
 challenge_id text PRIMARY KEY,
 owner_id text NOT NULL,
 login_id text NOT NULL,
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 launch_generation text NOT NULL,
 runtime_generation bigint NOT NULL CHECK(runtime_generation>0),
 connection_id text NOT NULL,
 identity_digest text NOT NULL,
 consent jsonb NOT NULL,
 expires_at timestamptz NOT NULL,
 accepted_at timestamptz
);
CREATE INDEX shell_host_consent_launch ON shell_host_consents(session_id,launch_generation,runtime_generation,connection_id,owner_id,identity_digest) WHERE accepted_at IS NOT NULL;
