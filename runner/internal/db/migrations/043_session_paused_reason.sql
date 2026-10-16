-- sessions.paused_reason: why a live session is paused waiting for a message. "restart": its
-- daemon restarted, cut off what it was doing, and did not pick it up again by itself (it was
-- waiting for the person, or had been continued once already); the daemon reports it
-- (session_paused). Cleared when the session next runs, ends or is revived. NULL: not paused.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS paused_reason text;
