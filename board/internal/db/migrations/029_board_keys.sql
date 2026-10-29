-- Forward-only: leave existing boards unassigned until an administrator confirms a suggestion.
-- Card numbers and every existing foreign key remain unchanged.
ALTER TABLE boards ADD COLUMN board_key text NOT NULL DEFAULT ''
    CHECK (board_key = '' OR board_key ~ '^[A-Z0-9]{2,5}$');
CREATE TABLE board_keys (
    key text PRIMARY KEY CHECK (key ~ '^[A-Z0-9]{2,5}$'),
    board_id uuid NOT NULL REFERENCES boards(id) ON DELETE CASCADE
);
CREATE INDEX board_keys_board_id ON board_keys(board_id);
CREATE UNIQUE INDEX boards_current_key ON boards(board_key) WHERE board_key <> '';
