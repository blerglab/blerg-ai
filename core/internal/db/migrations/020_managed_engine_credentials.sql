-- Idempotent: migrations may be replayed after a crash before migration recording.
ALTER TABLE user_credentials ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'opaque';
ALTER TABLE user_credentials ADD COLUMN IF NOT EXISTS generation uuid NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE user_credentials ADD COLUMN IF NOT EXISTS state text NOT NULL DEFAULT 'configured';
ALTER TABLE user_credentials ADD COLUMN IF NOT EXISTS expires_at timestamptz;
ALTER TABLE user_credentials ADD COLUMN IF NOT EXISTS refresh_claim uuid;
ALTER TABLE user_credentials ADD COLUMN IF NOT EXISTS claim_deadline timestamptz;
CREATE TABLE IF NOT EXISTS credential_login_attempts (
 account_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 engine text NOT NULL,
 attempt_id uuid NOT NULL DEFAULT gen_random_uuid(),
 session_id uuid NOT NULL,
 state text NOT NULL DEFAULT 'starting',
 ciphertext bytea,
 key_id text,
 user_code text NOT NULL DEFAULT '',
 verification_url text NOT NULL DEFAULT '',
 interval_seconds integer NOT NULL DEFAULT 5,
 expires_at timestamptz NOT NULL DEFAULT now()+interval '15 minutes',
 next_poll_at timestamptz NOT NULL DEFAULT now(),
 claim uuid,
 claim_deadline timestamptz,
 PRIMARY KEY(account_id,engine),
 UNIQUE(attempt_id)
);
CREATE TABLE IF NOT EXISTS credential_lifecycle_events (
 id bigserial PRIMARY KEY,
 account_id uuid NOT NULL,
 engine text NOT NULL,
 action text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS credential_lifecycle_events_created ON credential_lifecycle_events(created_at);
