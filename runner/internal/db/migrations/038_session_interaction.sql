-- sessions.interaction: whether a person is reading the session's chat as it works
-- ("interactive") or nobody is ("unattended": a board card run, a cron, a tool's job). Decided
-- by the server at start and told to the engine in its system prompt. '' is a session started
-- before this column: read as interactive when the person started it and unattended otherwise
-- (a cron's, or one a tool or the operator key started).
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS interaction text NOT NULL DEFAULT '';
