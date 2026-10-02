-- Session artifacts: files an agent hands the user with `blerg-runner publish`.
-- The bytes live on the runner data volume (<data dir>/artifacts/<session>/<id>/<name>); this table
-- is the index. A session's artifacts go with it (ON DELETE CASCADE); the files are removed by the
-- server, and by a prune for any directory whose session is gone.
CREATE TABLE IF NOT EXISTS session_artifacts (
  id           text PRIMARY KEY,
  session_id   uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  name         text NOT NULL,
  size         bigint NOT NULL,
  content_type text NOT NULL,
  sha256       text NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS session_artifacts_session_created_idx
  ON session_artifacts (session_id, created_at);
