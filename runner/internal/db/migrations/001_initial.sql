-- 001_initial.sql: initial schema

CREATE TABLE IF NOT EXISTS daemons (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text        NOT NULL,
    mode        text        NOT NULL,
    repos_root  text        NOT NULL,
    status      text        NOT NULL DEFAULT 'disconnected',
    last_seen_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sessions (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    daemon_id    uuid        NOT NULL REFERENCES daemons(id),
    status       text        NOT NULL DEFAULT 'starting',
    project_path text        NOT NULL,
    repo         text        NOT NULL,
    title        text,
    started_at   timestamptz NOT NULL DEFAULT now(),
    ended_at     timestamptz
);

CREATE INDEX IF NOT EXISTS sessions_daemon_id_idx ON sessions(daemon_id);
CREATE INDEX IF NOT EXISTS sessions_status_idx ON sessions(status);

CREATE TABLE IF NOT EXISTS session_events (
    id         bigserial   PRIMARY KEY,
    session_id uuid        NOT NULL REFERENCES sessions(id),
    type       text        NOT NULL,
    data       text        NOT NULL,
    seq        bigint      NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS session_events_session_id_seq_idx ON session_events(session_id, seq);

CREATE TABLE IF NOT EXISTS push_subscriptions (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    endpoint   text        NOT NULL UNIQUE,
    p256dh     text        NOT NULL,
    auth       text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
