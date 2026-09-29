-- 008_agent_sessions.sql: structured transcripts for agent-kind sessions.
-- agent_events is the append-only transcript; seq is assigned by the SERVER
-- on append (per-session, monotonic) and client_event_id is the daemon-side
-- idempotency key — re-sends after reconnect dedupe on it instead of
-- clobbering seq (the 004_remove_seq_unique lesson).
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'tmux';
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS parent_session_id uuid REFERENCES sessions(id);

CREATE TABLE IF NOT EXISTS agent_events (
    id              bigserial   PRIMARY KEY,
    session_id      uuid        NOT NULL REFERENCES sessions(id),
    seq             bigint      NOT NULL,
    client_event_id uuid        NOT NULL,
    kind            text        NOT NULL,
    payload         jsonb       NOT NULL,
    ts              timestamptz NOT NULL DEFAULT now(),
    UNIQUE (session_id, seq),
    UNIQUE (session_id, client_event_id)
);
CREATE INDEX IF NOT EXISTS agent_events_session_seq_idx ON agent_events(session_id, seq);
