-- Column audit: every column change (create, rename or property change, move, delete) with who made
-- it and, for a change a runner session made through its board access, which session. Kept apart
-- from card_events (card-scoped) and FK-free so a deleted column's history survives.
CREATE TABLE column_events (
    id             bigserial PRIMARY KEY,
    board_id       uuid NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    column_id      uuid NOT NULL,
    type           text NOT NULL CHECK (type IN ('created','updated','moved','deleted')),
    actor          text NOT NULL,
    actor_account  text NOT NULL DEFAULT '',  -- the person the change acts for (core on_behalf_of)
    actor_token    text NOT NULL DEFAULT '',  -- board token id, or core:<sub> for a core-issued token
    runner_session text NOT NULL DEFAULT '',  -- the runner session a board-access call came from
    data           jsonb NOT NULL DEFAULT '{}',
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX column_events_board_idx ON column_events (board_id, id);
