-- A session's explicit request that its owner review one published file or diff, pinned to the
-- exact version it names (blerg-runner review request). Only the session's signed-in owner decides
-- it; a newer version of the same file supersedes a pending one.
CREATE TABLE IF NOT EXISTS review_requests (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  session_id       uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  artifact_id      text NOT NULL,
  artifact_name    text NOT NULL,
  artifact_origin  text NOT NULL DEFAULT 'agent',
  artifact_version integer NOT NULL,
  artifact_view    text NOT NULL DEFAULT '',
  ask              text NOT NULL,
  state            text NOT NULL DEFAULT 'pending'
                   CHECK (state IN ('pending', 'approved', 'changes_requested', 'withdrawn', 'superseded')),
  note             text NOT NULL DEFAULT '',
  superseded_by    integer,
  decided_by       text,
  decided_at       timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS review_requests_session_idx ON review_requests (session_id, created_at);

-- At most one pending request per exact version: asking again returns the open one.
CREATE UNIQUE INDEX IF NOT EXISTS review_requests_one_pending_idx
  ON review_requests (session_id, artifact_id) WHERE state = 'pending';
