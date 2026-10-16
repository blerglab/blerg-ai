CREATE TABLE session_limit_resume (
 session_id uuid PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
 provider text NOT NULL DEFAULT '',
 enabled boolean NOT NULL DEFAULT false,
 auth_session_id text NOT NULL DEFAULT '',
 turn_id text NOT NULL DEFAULT '',
 cancelled_turn_id text NOT NULL DEFAULT '',
 reset_at timestamptz,
 state text NOT NULL DEFAULT 'disabled',
 reason text NOT NULL DEFAULT '',
 attempts integer NOT NULL DEFAULT 0,
 event_seq bigint NOT NULL DEFAULT 0,
 cancelled_at timestamptz,
 expires_at timestamptz,
 dispatched_at timestamptz,
 last_attempt_turn_id text NOT NULL DEFAULT '',
 attempt_id text NOT NULL DEFAULT ''
);
CREATE INDEX session_limit_resume_due ON session_limit_resume(reset_at) WHERE enabled AND state = 'pending';
