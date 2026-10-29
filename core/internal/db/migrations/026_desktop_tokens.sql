-- Desktop credentials are distinct from agent, browser and install credentials.
CREATE TABLE IF NOT EXISTS desktop_tokens (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 account_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 desktop_id uuid NOT NULL,
 ownership_revision bigint NOT NULL CHECK (ownership_revision > 0),
 token_hash text NOT NULL UNIQUE,
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL,
 revoked_at timestamptz
);
CREATE INDEX IF NOT EXISTS desktop_tokens_owner ON desktop_tokens(account_id, desktop_id);
