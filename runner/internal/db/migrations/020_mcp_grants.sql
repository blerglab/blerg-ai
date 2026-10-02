-- Per-session MCP grants (spec 5.3): which of a user's MCP connections a
-- session may use through the runner's MCP gateway, with a per-tool mode and
-- pinned hash. One row (and one gateway token) per (session, connection).
-- The raw token is never stored; token_hash is its SHA-256.
CREATE TABLE session_mcp_grants (
  session_id    uuid NOT NULL,
  connection_id uuid NOT NULL,
  name          text NOT NULL,
  account_id    text NOT NULL,
  tools         jsonb NOT NULL,                -- {"tool": {"mode": "allow"|"propose", "hash": "..."}}
  url_snapshot  text NOT NULL,                 -- connection URL at grant time
  proof_kind    text NOT NULL CHECK (proof_kind IN ('token_id','session_id')),
  proof_value   text NOT NULL,
  token_hash    bytea NOT NULL UNIQUE,
  call_budget   int NOT NULL CHECK (call_budget >= 0),
  calls_used    int NOT NULL DEFAULT 0,
  created_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (session_id, connection_id)
);

-- One row per gateway call. Never holds arguments or results. Rows older than
-- 90 days are pruned by the gateway's background ticker.
CREATE TABLE mcp_call_log (
  id            bigserial PRIMARY KEY,
  session_id    uuid NOT NULL,
  connection_id uuid NOT NULL,
  tool          text NOT NULL DEFAULT '',
  mode          text NOT NULL,
  outcome       text NOT NULL,
  duration_ms   int NOT NULL DEFAULT 0,
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mcp_call_log_created_at ON mcp_call_log (created_at);
CREATE INDEX mcp_call_log_session ON mcp_call_log (session_id, created_at);
