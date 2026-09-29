-- 013: runner session contract v1 (agent-contract spec §3).
--
-- (a) runner_idempotency: POST /api/runner/start honours an Idempotency-Key
--     header. A row remembers which session a (scope, key) pair produced and a
--     hash of the request that produced it, so a retry replays the original
--     202 while the same key with a different body is a 409. Rows are ignored
--     and pruned after 24 h — the key is a retry window, not a permanent
--     ledger.
-- (b) sessions.callback_url / callback_secret: the completion webhook. The
--     secret is stored raw because it must sign the delivery (HMAC-SHA256);
--     it is never returned by any endpoint.
-- (c) sessions.token_id: the agent token that started the session, sent to
--     blerg-core on every credential fetch so core can check the token is
--     still live (core Task 5). Distinct from spawning_account_id, which is
--     the ACCOUNT the session runs as.
-- (d) sessions.git_url: the clone URL the session was actually started with,
--     so the result endpoint can report it instead of guessing at a URL that
--     a cross-org override may have replaced.
CREATE TABLE IF NOT EXISTS runner_idempotency (
    scope        text        NOT NULL,
    key          text        NOT NULL,
    request_hash text        NOT NULL,
    session_id   uuid        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, key)
);
CREATE INDEX IF NOT EXISTS runner_idempotency_created_at_idx ON runner_idempotency (created_at);

ALTER TABLE sessions ADD COLUMN IF NOT EXISTS callback_url text;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS callback_secret text;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS token_id text;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS git_url text;
