-- An owner-only ask (an engine's command approval) is answered only by the
-- session's signed-in owner, never by the daemon, a board token or another viewer.
ALTER TABLE messages ADD COLUMN IF NOT EXISTS owner_only boolean NOT NULL DEFAULT false;
