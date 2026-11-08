-- BLG-474 review: the CHECK constraint duplicated the account theme vocabulary (110_account_theme.sql)
-- and went stale the moment packages/settings/theme.ts grew past the original six IDs — saving any
-- newer theme failed at this UPDATE with no clue why. contracts/theme.Valid is already the single
-- place that vocabulary lives and is already checked before every write that reaches this column
-- (core/internal/api/theme.go); drop the duplicate instead of re-widening it by hand again next time.
ALTER TABLE accounts DROP CONSTRAINT IF EXISTS accounts_theme_check;
