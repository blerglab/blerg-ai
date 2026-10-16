-- sessions.background_tasks: how many background tasks (a background shell, a monitor, a background
-- subagent) the session's agent has running, kept from its background_task_started and
-- background_task_done events as they are recorded, so the sessions list can show it for a session
-- nobody has open. Back to 0 when the session is revived or ends.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS background_tasks integer NOT NULL DEFAULT 0;
