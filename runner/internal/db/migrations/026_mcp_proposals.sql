-- 026: MCP proposals (spec 9).
--
-- A tool the person put in `propose` mode is never forwarded to the upstream server. The gateway
-- freezes the call here instead, and a signed-in person approves or rejects it later. The frozen
-- `arguments` are the authoritative record: they are what the UI shows and what an approval sends,
-- byte for byte as JSON. `url_snapshot` and `tool_hash` pin the connection address and the tool
-- definition the agent saw; an approval is refused when either no longer matches.
--
-- States: pending -> executing -> done | failed | unknown, pending -> rejected | expired.
-- `unknown` is an ambiguous outcome (the request may have been delivered); it is never retried
-- automatically and waits for a human decision.
--
-- session_id and cron_id are deliberately NOT foreign keys: a proposal outlives its session (and
-- a deleted cron) until it is decided or expires.
CREATE TABLE IF NOT EXISTS mcp_proposals (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id      text NOT NULL,
  session_id      uuid,
  cron_id         uuid,
  connection_id   uuid NOT NULL,
  connection_name text NOT NULL,
  url_snapshot    text NOT NULL,
  tool            text NOT NULL,
  tool_hash       text NOT NULL,
  arguments       jsonb NOT NULL CHECK (pg_column_size(arguments) <= 65536),
  agent_summary   text,
  state           text NOT NULL DEFAULT 'pending'
    CHECK (state IN ('pending','executing','done','rejected','expired','failed','unknown')),
  result          jsonb,
  created_at      timestamptz NOT NULL DEFAULT now(),
  decided_at      timestamptz,
  decided_by      text
);

-- The owner's list, by state, newest first.
CREATE INDEX IF NOT EXISTS mcp_proposals_account_state ON mcp_proposals (account_id, state, created_at DESC);
-- The expiry job and the pending cap only look at open rows.
CREATE INDEX IF NOT EXISTS mcp_proposals_open ON mcp_proposals (created_at) WHERE state IN ('pending', 'executing');
-- Retention prunes decided rows by decision time.
CREATE INDEX IF NOT EXISTS mcp_proposals_decided ON mcp_proposals (decided_at) WHERE decided_at IS NOT NULL;
