-- Idempotent on purpose (as are 016 and 017): core applies a migration and then records it in a
-- separate statement, so a crash in between re-runs the file on the next start.
-- MCP connections (docs/design/ai-crons.md 4.1): a person's remote MCP servers, with the
-- credential (none, a static header value, or an OAuth token bundle) encrypted by the same key
-- backend as user_credentials. default_tools holds the person's per-tool defaults; the runner's
-- per-session grants are the authoritative selection.
CREATE TABLE IF NOT EXISTS mcp_connections (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id        uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    name              text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,31}$'),
    url               text NOT NULL,
    auth_kind         text NOT NULL CHECK (auth_kind IN ('none','static','oauth')),
    header_name       text,                        -- static only, validated in the service
    secret_ciphertext bytea,                       -- static secret, or the oauth token bundle
    key_id            text,
    oauth_meta        jsonb,                       -- issuer, resource, client_id, endpoints (no secrets)
    default_tools     jsonb NOT NULL DEFAULT '{}', -- {"tool": {"mode": "allow"|"propose", "hash": "..."}}
    status            text NOT NULL DEFAULT 'ok' CHECK (status IN ('ok','needs_auth','error')),
    last_verified_at  timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (account_id, name)
);

-- One row per secret hand-out to the runner. No foreign keys on purpose: the audit trail outlives
-- the connection and the account. Pruned after 180 days by a daily job.
CREATE TABLE IF NOT EXISTS mcp_secret_access_log (
    id                    bigserial PRIMARY KEY,
    account_id            uuid NOT NULL,
    connection_id         uuid,
    fetched_by_session_id uuid,
    fetched_by_token_id   uuid,
    fetched_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS mcp_secret_access_log_fetched_at ON mcp_secret_access_log (fetched_at);

-- Single-use, server-side state of the OAuth connect flow: written when a flow starts, consumed
-- (deleted) by the callback, and pruned once expired.
CREATE TABLE IF NOT EXISTS mcp_oauth_state (
    state_hash    bytea PRIMARY KEY,
    account_id    uuid NOT NULL,
    sid           text NOT NULL,
    code_verifier text NOT NULL,
    draft         jsonb NOT NULL,
    issuer        text NOT NULL,
    resource      text NOT NULL,
    redirect_uri  text NOT NULL,
    expires_at    timestamptz NOT NULL
);
