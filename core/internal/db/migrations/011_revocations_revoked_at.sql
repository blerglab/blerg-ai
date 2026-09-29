-- Revocations carry the moment they were issued so consumers (board/runner, and
-- core's own snapshot checker) can scope a "sub"/"lineage" entry to tokens issued
-- at or before it: a token minted by a re-login AFTER a password change /
-- logout-all / theft detection verifies even while a consumer's cached list
-- still holds the entry (previously a deterministic lockout of up to one poll
-- interval). Existing rows backfill to now(): every token they were meant to
-- kill was issued before this migration ran, so nothing already revoked is
-- resurrected.
ALTER TABLE revocations ADD COLUMN revoked_at timestamptz NOT NULL DEFAULT now();
