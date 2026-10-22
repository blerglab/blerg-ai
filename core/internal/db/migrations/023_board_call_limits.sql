-- Idempotent on purpose (as are 015 onward): core applies a migration and then records it in a
-- separate statement, so a crash in between re-runs the file on the next start.
-- The limits on session board calls (0.4.5), set by an administrator under Settings. One row at
-- most; no row means the defaults (identity.DefaultBoardLimits). Core enforces live_exchange_tokens
-- on every exchange; the runner reads the other three (POST /internal/board-limits) and caches them.
-- The CHECKs ride the column clauses, so a re-run adds no second constraint.
CREATE TABLE IF NOT EXISTS board_call_limits (
    id                    boolean PRIMARY KEY DEFAULT true CHECK (id),
    calls_per_minute      integer NOT NULL CHECK (calls_per_minute BETWEEN 1 AND 10000),
    calls_in_flight       integer NOT NULL CHECK (calls_in_flight BETWEEN 1 AND 64),
    unrevoked_per_account integer NOT NULL CHECK (unrevoked_per_account BETWEEN 1 AND 1000),
    live_exchange_tokens  integer NOT NULL CHECK (live_exchange_tokens BETWEEN 10 AND 10000),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    updated_by            uuid
);
