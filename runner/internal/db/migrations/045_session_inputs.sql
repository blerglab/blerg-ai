-- Durable browser inputs. Keep settled ids to make a retry idempotent.
CREATE TABLE session_inputs (
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 client_id text NOT NULL,
 frame jsonb NOT NULL,
 event_id uuid NOT NULL,
 event_seq bigint NOT NULL DEFAULT 0,
 accepted_at timestamptz NOT NULL DEFAULT now(),
 settled_at timestamptz,
 PRIMARY KEY (session_id, client_id)
);
CREATE INDEX session_inputs_pending ON session_inputs(session_id, accepted_at) WHERE settled_at IS NULL;
