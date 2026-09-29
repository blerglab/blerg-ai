-- 007_messages.sql: session→user messages (updates, blocking asks, async notes).
-- `status='open'` means the message needs the user; `update` rows are created
-- already-answered. `answer` is overloaded: NULL (update), '' (closed/expired/
-- session-ended), or real text (a genuine reply).
CREATE TABLE IF NOT EXISTS messages (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id  uuid        NOT NULL REFERENCES sessions(id),
    kind        text        NOT NULL,                 -- 'update' | 'ask' | 'note'
    body        text        NOT NULL,
    status      text        NOT NULL DEFAULT 'open',  -- 'open' | 'answered'
    answer      text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    answered_at timestamptz
);
CREATE INDEX IF NOT EXISTS messages_session_id_idx ON messages(session_id);
CREATE INDEX IF NOT EXISTS messages_status_idx ON messages(status);
