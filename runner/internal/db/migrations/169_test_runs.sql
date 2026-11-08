-- Project test history is independent of the short-lived gate dataset.
CREATE TABLE test_runs (
 workspace_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 member_id text NOT NULL CHECK(length(member_id) BETWEEN 1 AND 128),
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 run_id uuid NOT NULL,
 revision bigint NOT NULL CHECK(revision > 0),
 finished_at timestamptz NOT NULL,
 report jsonb NOT NULL,
 observed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(workspace_id,member_id,run_id)
);
CREATE INDEX test_runs_recent ON test_runs(workspace_id,member_id,finished_at DESC,run_id);
