-- Per-role model overrides. Discussion and board-header chat sessions are
-- thinking work, not execution: they can run on a stronger model than the
-- board's worker default (and reviews already could, via reviewer_model).
-- Empty means "unset" — fall back to boards.model, same as reviewer_model.
ALTER TABLE boards ADD COLUMN discuss_model text NOT NULL DEFAULT '';
ALTER TABLE boards ADD COLUMN chat_model text NOT NULL DEFAULT '';

-- Per-card override: wins over every board-level model for that card's
-- sessions. Empty = unset, resolve from the board.
ALTER TABLE cards ADD COLUMN model text NOT NULL DEFAULT '';
