-- Sanitized record source; raw reports/logs/host metadata are never retained.
CREATE TABLE live_gate_reports (
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 run_id uuid NOT NULL,
 revision bigint NOT NULL CHECK(revision>0),
 report jsonb NOT NULL,
 observed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(session_id,run_id)
);
ALTER TABLE live_dataset_attempts DROP CONSTRAINT live_dataset_attempts_source_check;
ALTER TABLE live_dataset_attempts ADD CONSTRAINT live_dataset_attempts_source_check CHECK(source IN
 ('blerg.workers','blerg.engine.usage','blerg.cards.merged','blerg.gates.durations'));
