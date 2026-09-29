-- 002_model.sql: add model column to sessions
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS model text;
