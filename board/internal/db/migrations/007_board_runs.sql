-- Run-the-board: a server-side dispatcher drains the ready column one card
-- at a time; auto_merge lets an adversarially-approved PR land without a
-- human click.

ALTER TABLE cards ADD COLUMN auto_merge boolean NOT NULL DEFAULT true;
ALTER TABLE cards ADD COLUMN run_attempts int NOT NULL DEFAULT 0;
ALTER TABLE cards ADD COLUMN stuck_at timestamptz;

CREATE TABLE board_runs (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    board_id    uuid NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    state       text NOT NULL DEFAULT 'running' CHECK (state IN ('running', 'stopped', 'done')),
    concurrency int NOT NULL DEFAULT 1,
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    cards_done  int NOT NULL DEFAULT 0,
    cards_stuck int NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX board_runs_one_active ON board_runs (board_id) WHERE state = 'running';
