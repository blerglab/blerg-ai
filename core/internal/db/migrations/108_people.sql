-- People invited by an administrator are members until an explicit role change.
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS display_name text NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS person_invites (
 account_id uuid PRIMARY KEY REFERENCES accounts(id),
 code_hash text NOT NULL UNIQUE,
 expires_at timestamptz NOT NULL,
 used_at timestamptz
);
