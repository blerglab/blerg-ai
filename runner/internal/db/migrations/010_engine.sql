-- 010_engine.sql: track which CLI engine ("" (claude) | "codex" | "hermes")
-- a session was launched with, so a cluster session that disconnects and
-- needs to be resumed (resumeClusterSession) recreates its Job with the same
-- engine instead of silently falling back to claude.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS engine text NOT NULL DEFAULT '';
