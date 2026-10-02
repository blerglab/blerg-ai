-- Who put a session file there: 'agent' (blerg-runner publish, the default, so every existing row
-- stays an agent file) or 'user' (a person attached it from the chat box; uploaded_by is their
-- account id). The agent fetches user files with `blerg-runner fetch`.
ALTER TABLE session_artifacts ADD COLUMN IF NOT EXISTS origin text NOT NULL DEFAULT 'agent';
ALTER TABLE session_artifacts ADD COLUMN IF NOT EXISTS uploaded_by text;
