-- Existing keys are never renamed silently. Invalid pre-release keys must be
-- renamed by an administrator before upgrading; aliases beginning with a digit
-- are no longer card references and must be removed before this migration.
ALTER TABLE boards DROP CONSTRAINT boards_board_key_check;
ALTER TABLE boards ADD CONSTRAINT boards_board_key_check
    CHECK (board_key = '' OR board_key ~ '^[A-Z][A-Z0-9]{1,4}$');
ALTER TABLE board_keys DROP CONSTRAINT board_keys_key_check;
ALTER TABLE board_keys ADD CONSTRAINT board_keys_key_check
    CHECK (key ~ '^[A-Z][A-Z0-9]{1,4}$');
