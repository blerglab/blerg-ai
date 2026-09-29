-- 008_session_events_index.sql: restore an index on session_events for
-- session_id lookups. Migration 004 dropped the only index that covered
-- session_id (a unique index on (session_id, seq)) to fix a correctness bug,
-- but left session_events with no index at all, so every tail read
-- (GetSessionEventsTail) full-scans and sorts the table. Add a non-unique
-- index on (session_id, id) — id is the walk order used by the keyset-
-- paginated tail query, so this covers both the WHERE and ORDER BY.
CREATE INDEX IF NOT EXISTS session_events_session_id_id_idx
    ON session_events (session_id, id DESC);
