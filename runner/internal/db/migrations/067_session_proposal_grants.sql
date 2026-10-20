-- 067: session permissions for proposals. The owner lets one session file cards on one board, or
-- run one MCP write tool of one connection, without asking each time. A grant is never broader
-- than that scope, belongs to the session's owner, and ends with the session (or its parent's
-- discard). proof_session_id is the creator's own login session: an auto-approval acts on it
-- exactly as a manual approval acts on the approver's, so signing out stops auto-approval.
CREATE TABLE IF NOT EXISTS session_proposal_grants (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  session_id            uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  account_id            text NOT NULL,
  kind                  text NOT NULL CHECK (kind IN ('board_file','mcp_tool')),
  board_id              text NOT NULL DEFAULT '',
  board_name            text NOT NULL DEFAULT '',
  connection_id         uuid,
  connection_name       text NOT NULL DEFAULT '',
  tool                  text NOT NULL DEFAULT '',
  -- The MCP tool's pinned definition the grant was given on: a changed definition asks again.
  tool_hash             text NOT NULL DEFAULT '',
  proof_session_id      text NOT NULL CHECK (proof_session_id <> ''),
  project_scope         text NOT NULL DEFAULT '',
  created_via           text NOT NULL CHECK (created_via IN ('card','panel','launch')),
  created_from_proposal uuid REFERENCES mcp_proposals(id) ON DELETE SET NULL,
  created_at            timestamptz NOT NULL DEFAULT now(),
  revoked_at            timestamptz,
  revoked_reason        text,
  CHECK ((kind = 'board_file' AND board_id <> '' AND connection_id IS NULL AND tool = '')
      OR (kind = 'mcp_tool' AND board_id = '' AND connection_id IS NOT NULL AND tool <> '' AND tool_hash <> ''))
);
CREATE UNIQUE INDEX IF NOT EXISTS session_proposal_grants_live_scope_idx
  ON session_proposal_grants (session_id, kind, board_id, COALESCE(connection_id::text, ''), tool)
  WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS session_proposal_grants_session_idx ON session_proposal_grants (session_id, created_at);
-- A repeated filing from the same session finds its recent twin (idempotent filing).
CREATE INDEX IF NOT EXISTS mcp_proposals_session_recent_idx ON mcp_proposals (session_id, created_at)
  WHERE session_id IS NOT NULL;

-- The grant that approved a proposal on its own, recorded with the decision (audit).
ALTER TABLE mcp_proposals ADD COLUMN IF NOT EXISTS approved_by_grant uuid
  REFERENCES session_proposal_grants(id) ON DELETE SET NULL;

-- The History tab shows the cards a session's approved proposals created (proposal_decided events),
-- read beside 041's reel kinds. A partial index keeps that read off the session's other events.
CREATE INDEX IF NOT EXISTS agent_events_proposal_decided_idx
  ON agent_events (session_id, seq) WHERE kind = 'proposal_decided';

-- The hourly cap on what one grant approves counts its recent decisions.
CREATE INDEX IF NOT EXISTS mcp_proposals_approved_by_grant_idx ON mcp_proposals (approved_by_grant, decided_at)
  WHERE approved_by_grant IS NOT NULL;
