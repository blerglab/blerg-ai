-- 018: why a session ended, and who ended it.
--
-- status/ended_at say THAT a session is over; error_reason only explains a
-- failure. Nothing recorded whether a person stopped it, an agent token did,
-- the one-shot auto-stop did, or the process simply exited — so the UI could
-- not say "Ended by you" and a broker could not tell a stop from a crash.
--
-- end_reason is a short code from a fixed vocabulary (sessionend.go in the
-- server package lists it); it is never free text from a caller. ended_by_kind
-- is the principal kind of the actor that ended the session (human, agent,
-- service, runner_key) and is NULL when nobody did — a process exit, a failed
-- Job, the reconciler. ended_by_account is that actor's core account id (a
-- human's sub, an agent token's on_behalf_of); the same opaque id already
-- stored in spawning_account_id, never a name or an email.
--
-- All three are written once, first write wins: a stop that is recorded when
-- it is requested is not overwritten by the process exit it causes. They are
-- cleared only when the session is revived (a reconnecting daemon reports it
-- alive, a cluster session resumes), exactly like error_reason.
--
-- end_recorded_at is when the attribution was written. A stop is recorded
-- when it is REQUESTED, before the session has actually ended; if the session
-- is still reported alive well after that (the kill was lost or ignored), the
-- pending stop is dropped (UpdateSessionStatus, ExpirePendingSessionEnd) so a
-- much later, unrelated ending is not blamed on it.
--
-- Rows ended before this migration have NULL everywhere; readers treat that as
-- "no recorded reason" and fall back to what they showed before.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS end_reason text;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS ended_by_kind text;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS ended_by_account text;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS end_recorded_at timestamptz;
