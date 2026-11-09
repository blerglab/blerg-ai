CREATE TABLE board_releases (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 board_id uuid NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
 name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 120),
 target_date date,
 status text NOT NULL DEFAULT 'planned' CHECK (status IN ('planned','in progress','released')),
 UNIQUE(board_id,id), UNIQUE(board_id,name)
);
ALTER TABLE cards ADD COLUMN release_id uuid;
ALTER TABLE cards ADD CONSTRAINT cards_board_release_fk FOREIGN KEY(board_id,release_id) REFERENCES board_releases(board_id,id);
CREATE INDEX cards_release_idx ON cards(release_id) WHERE release_id IS NOT NULL;
-- Retain column progress even after archiving clears the card's column.
CREATE TABLE release_done_cards (
 release_id uuid NOT NULL REFERENCES board_releases(id) ON DELETE CASCADE,
 card_id uuid NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
 column_name text NOT NULL,
 PRIMARY KEY(release_id,card_id)
);
