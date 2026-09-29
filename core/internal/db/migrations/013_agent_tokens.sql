-- Agent tokens (spec §2): named, scoped, revocable credentials a user mints in the browser so
-- an external automated tool never needs database access or a shared static key.
--
-- Only the SHA-256 hash of the raw token is stored, never the token itself — the value is
-- returned exactly once, by POST /api/tokens, and is unrecoverable afterwards. The hash column
-- is UNIQUE so the same signed token can never be registered twice.
--
-- caps is the already-intersected capability list (preset caps ∩ the owner's PlatformRoleCaps
-- at mint time), stored so GET /api/tokens can show exactly what the token carries without
-- re-deriving it from a preset name whose definition may have changed since.
--
-- Revocation is two-part: revoked_at here (what the owner's list shows) plus a
-- revocations("sub", id) row (what every consumer already polls). ON DELETE CASCADE mirrors
-- human_sessions/user_credentials: deleting an account takes its tokens with it.
CREATE TABLE agent_tokens (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id   uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    name         text NOT NULL,
    aud          text NOT NULL,
    caps         text[] NOT NULL,
    token_hash   text NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_used_at timestamptz,
    revoked_at   timestamptz
);
CREATE INDEX agent_tokens_account_id_idx ON agent_tokens(account_id);

-- A personal credential fetched for an agent-token session has no human session to attribute
-- it to, so fetched_by_session_id stays NULL and the token id goes here instead. Keeping the
-- two in separate columns means an auditor can always tell which kind of principal performed
-- the fetch rather than having to guess which table a uuid belongs to.
ALTER TABLE credential_access_log ADD COLUMN fetched_by_token_id uuid;
