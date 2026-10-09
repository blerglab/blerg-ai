-- sessions.restrict_tools: the session runs with the restricted tool set (file tools only, no
-- ambient MCP, none of the person's settings or plugins). Set for unattended runs: a cron, or a
-- session the board starts with MCP connections. A session the person launches from the launch
-- sheet is not restricted, connections or not (docs/design/interactive-mcp-sessions.md).
-- Recorded so a cluster resume rebuilds the same Job. Every session with a grant before this
-- migration ran restricted, and stays so.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS restrict_tools boolean NOT NULL DEFAULT false;
UPDATE sessions SET restrict_tools = true
 WHERE cron_id IS NOT NULL
    OR id IN (SELECT session_id FROM session_mcp_grants);
