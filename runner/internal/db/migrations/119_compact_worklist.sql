-- Current state is separate from the change log. Sparse checkpoints bound historical reads.
CREATE TABLE session_work_state (
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 kind text NOT NULL,
 seq bigint NOT NULL,
 revision bigint NOT NULL,
 payload jsonb NOT NULL,
 PRIMARY KEY(session_id,kind)
);
CREATE TABLE checklist_checkpoints (
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 seq bigint NOT NULL,
 payload jsonb NOT NULL,
 PRIMARY KEY(session_id,seq)
);
CREATE INDEX agent_events_work_rate_idx ON agent_events(session_id,kind,ts)
 WHERE kind IN ('checklist','topic','ticket');
