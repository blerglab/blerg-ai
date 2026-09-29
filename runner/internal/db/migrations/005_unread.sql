-- 005_unread.sql: track whether a session has unread output since the user
-- last viewed it. Defaults to false; SET to true on new output, false on view.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS unread boolean NOT NULL DEFAULT false;
