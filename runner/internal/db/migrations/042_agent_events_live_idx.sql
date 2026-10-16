-- The session panel's Live tab (GET /api/sessions/{id}/live) reads eight kinds of event of one session:
-- the agent's asks and their answers, the turn ends, the subagents and the background tasks. 041's
-- index has no asks and no background tasks; without this one a read on a long session tests every
-- event's kind on its heap tuple. kind is an index column, so a read of some of the kinds (the live
-- events but the subagent updates, or the updates alone) walks only their entries: on a real session
-- the updates are over half the events. Built inside the migration transaction, as 041's is: it holds writes
-- to agent_events while it builds, once, at the first start of a server with this migration.
CREATE INDEX IF NOT EXISTS agent_events_live_idx
    ON agent_events (session_id, kind, seq)
    WHERE kind IN ('ask', 'user_message', 'turn_done', 'subagent_started', 'subagent_update', 'subagent_done', 'background_task_started', 'background_task_done');
