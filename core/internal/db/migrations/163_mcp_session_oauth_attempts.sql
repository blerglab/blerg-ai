CREATE TABLE IF NOT EXISTS mcp_session_oauth_attempts (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 account_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 sid uuid NOT NULL,
 draft jsonb NOT NULL,
 stage text NOT NULL DEFAULT 'preview' CHECK(stage IN ('preview','starting','pending','exchanging','completed','failed','canceled','expired')),
 expires_at timestamptz NOT NULL,
 state_hash bytea UNIQUE,
 verifier_ciphertext bytea,
 verifier_key_id text,
 client_ciphertext bytea,
 client_key_id text,
 connection_id uuid,
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK ((verifier_ciphertext IS NULL)=(verifier_key_id IS NULL)),
 CHECK ((client_ciphertext IS NULL)=(client_key_id IS NULL)),
 CHECK (stage<>'pending' OR (state_hash IS NOT NULL AND verifier_ciphertext IS NOT NULL AND client_ciphertext IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS mcp_session_oauth_one_attempt ON mcp_session_oauth_attempts
 (account_id,(draft->'authorization'->'binding'->>'runner_installation_id'),(draft->'authorization'->'binding'->>'runner_session_id'),(draft->'authorization'->'definition'->>'id'))
 WHERE stage IN ('preview','starting','pending','exchanging');
CREATE INDEX IF NOT EXISTS mcp_session_oauth_expiry ON mcp_session_oauth_attempts(expires_at);
