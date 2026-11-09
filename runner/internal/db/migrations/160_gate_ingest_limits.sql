CREATE TABLE live_gate_ingest_limits (
 session_id uuid PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
 window_started timestamptz NOT NULL,
 requests integer NOT NULL CHECK(requests>0)
);
CREATE INDEX live_gate_reports_retention ON live_gate_reports(observed_at);
