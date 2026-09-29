-- 012: (a) board_tokens rows may belong to a session with no board — the
-- per-session messaging token that replaces the daemon master token in every
-- session's environment (desktop-safety C1/C2); (b) sessions.error_reason
-- (Task 6) — the daemon's spawn failure text, so an "error" row explains itself;
-- (c) sessions.runtime / skip_permissions (Task 5) — the posture a session was
-- launched with, so the UI can say what it runs as.
ALTER TABLE board_tokens ALTER COLUMN board_id DROP NOT NULL;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS error_reason text;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS runtime text;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS skip_permissions boolean NOT NULL DEFAULT false;
