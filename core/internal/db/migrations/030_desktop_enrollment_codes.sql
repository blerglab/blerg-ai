-- One-time codes that let a person add a new desktop (0.5.0, card #193). Only a hash of the
-- code is stored. A code is single use, expires, and its owner can revoke it.
CREATE TABLE IF NOT EXISTS desktop_enrollment_codes (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 account_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 code_hash text NOT NULL UNIQUE,
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL,
 used_at timestamptz,
 revoked_at timestamptz
);
CREATE INDEX IF NOT EXISTS desktop_enrollment_codes_account ON desktop_enrollment_codes(account_id, created_at);
-- Who did what to a desktop or an enrollment code. Never holds a code or a credential.
-- No foreign keys: the trail outlives the account, code and desktop it names.
CREATE TABLE IF NOT EXISTS desktop_audit (
 id bigserial PRIMARY KEY,
 actor_id text NOT NULL,
 action text NOT NULL,
 subject_id text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS desktop_audit_actor ON desktop_audit(actor_id, created_at);
