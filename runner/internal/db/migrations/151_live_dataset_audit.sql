-- Source records remain authoritative. Rebuild bounded snapshots after restart;
-- subscriptions are audited at open/close, without parameters or payload rows.
CREATE TABLE live_dataset_attempts (
 id uuid PRIMARY KEY,
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 owner_id text NOT NULL,
 source text NOT NULL CHECK (source IN ('blerg.workers','blerg.engine.usage','blerg.cards.merged')),
 started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 finished_at timestamptz,
 outcome text NOT NULL DEFAULT 'interrupted',
 row_count integer NOT NULL DEFAULT 0
);
CREATE INDEX live_dataset_attempts_retention ON live_dataset_attempts(started_at);
