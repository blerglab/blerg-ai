-- The session panel's Checklist, Plan and Ticket sections (GET /api/sessions/{id}/live) read three
-- small sets of one session's events, each through its own partial index rather than the live kinds
-- of 042 (a ticket event carries the whole ticket, up to 256 KiB; the live events are read on every
-- refresh):
--   - the newest checklist event (`blerg-runner checklist`): its payload is the full list;
--   - the ticket events (`blerg-runner pin` / `unpin`), folded into what is pinned now;
--   - the engine's own plan: Claude Code's TodoWrite, TaskCreate and TaskUpdate calls, and the results
--     that name a created task's id ("Task #3 created successfully: …").
-- Built inside the migration transaction, as 042's is: once, at the first start of a server with it.
CREATE INDEX IF NOT EXISTS agent_events_checklist_idx
    ON agent_events (session_id, seq DESC) WHERE kind = 'checklist';
CREATE INDEX IF NOT EXISTS agent_events_ticket_idx
    ON agent_events (session_id, seq) WHERE kind = 'ticket';
CREATE INDEX IF NOT EXISTS agent_events_plan_idx
    ON agent_events (session_id, seq)
    WHERE (kind = 'tool_call' AND payload->>'tool' IN ('TodoWrite', 'TaskCreate', 'TaskUpdate'))
       OR (kind = 'tool_result' AND payload->>'output' LIKE 'Task #%');
