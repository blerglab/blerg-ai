-- 006_starred.sql: track whether a session has been starred (pinned) by the user.
-- Defaults to false; SET to true to pin, false to unpin.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS starred boolean NOT NULL DEFAULT false;
