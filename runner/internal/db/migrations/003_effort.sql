-- 003_effort.sql: add effort column to sessions
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS effort text;
