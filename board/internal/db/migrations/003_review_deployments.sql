-- Adversarial review + deployment signals.

-- Sessions get a role: the worker drives the card; a reviewer session is
-- spawned automatically when a PR-bearing card enters review.
ALTER TABLE runner_sessions ADD COLUMN role text NOT NULL DEFAULT 'worker'
    CHECK (role IN ('worker', 'reviewer'));

-- Boards declare deploy environments; deployment events land per env.
ALTER TABLE boards ADD COLUMN environments jsonb NOT NULL DEFAULT '[]';
-- environments: [{"name":"prod","url":"https://blerg-board.example.com"}, ...]

CREATE TABLE deployments (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    board_id   uuid NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    env        text NOT NULL,
    sha        text NOT NULL DEFAULT '',
    status     text NOT NULL DEFAULT 'ok' CHECK (status IN ('ok', 'deploying', 'failed')),
    detail     text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX deployments_board_env_idx ON deployments (board_id, env, created_at DESC);
