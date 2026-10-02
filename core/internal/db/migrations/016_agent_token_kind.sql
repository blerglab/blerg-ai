-- Agent token kind. 'token' is an ordinary copyable agent token (a signed JWT the owner holds).
-- 'cron' is the identity of a scheduled run: the row exists so the token id can be proven live,
-- revoked and counted against the per-account cap, but NO signed JWT was ever produced for it and
-- token_hash is a random value that matches nothing. Cron rows are hidden from GET /api/tokens.
ALTER TABLE agent_tokens ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'token'
    CHECK (kind IN ('token', 'cron'));
