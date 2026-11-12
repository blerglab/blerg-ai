-- Server-owned launch identity; agent pins and title edits cannot change it.
CREATE TABLE session_capture_launch_cards (
 session_id uuid PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
 board_id text NOT NULL,
 card_number integer NOT NULL CHECK(card_number > 0)
);
