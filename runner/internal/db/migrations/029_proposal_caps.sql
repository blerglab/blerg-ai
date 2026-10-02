-- 029: indexes for the per-session and per-cron caps on pending proposals.
--
-- InsertProposalLimited counts an account's pending proposals per session and per cron under the
-- account's advisory lock. Both counts only look at pending rows.
CREATE INDEX IF NOT EXISTS mcp_proposals_pending_session ON mcp_proposals (session_id) WHERE state = 'pending' AND session_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS mcp_proposals_pending_cron ON mcp_proposals (cron_id) WHERE state = 'pending' AND cron_id IS NOT NULL;
