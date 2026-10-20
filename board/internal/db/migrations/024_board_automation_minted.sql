-- "Run as me": when Blerg minted a board's automation token (the board asked blerg-core for a
-- run-sessions token on the signed-in person's behalf), this is that token's id in core. Replacing
-- or removing the token, or deleting the board, revokes it in core. NULL for a pasted token, which
-- is the person's own and is never revoked by the board.
ALTER TABLE boards ADD COLUMN IF NOT EXISTS automation_token_minted_id text;
