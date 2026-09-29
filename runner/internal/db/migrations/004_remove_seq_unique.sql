-- 004_remove_seq_unique.sql: drop the unique index on (session_id, seq) so that
-- output events from a reattached daemon (seq restarting at 1) are not silently
-- dropped by ON CONFLICT DO NOTHING, which was leaving history stuck at the
-- pre-restart state and causing garbled terminal display on session switch.
DROP INDEX IF EXISTS session_events_session_id_seq_idx;
