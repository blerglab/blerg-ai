-- Global time paging and FK deletion/pruning paths must not scan retained JSON.
CREATE INDEX test_runs_finished_at ON test_runs(finished_at DESC,run_id DESC,workspace_id DESC,member_id DESC);
CREATE INDEX test_runs_session_id ON test_runs(session_id);
