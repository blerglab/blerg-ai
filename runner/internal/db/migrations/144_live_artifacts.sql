-- Live files (BLG-470): a file published with `blerg-runner publish --live` is updated in place on
-- every republish instead of growing a new version. `live` marks the one row that is the file's
-- current content; `updated_at` is when that content last changed (its own stamp, separate from
-- created_at, so "Updated 2 min ago" is truthful after an in-place overwrite).
ALTER TABLE session_artifacts ADD COLUMN IF NOT EXISTS live boolean NOT NULL DEFAULT false;
ALTER TABLE session_artifacts ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();

-- The at-most-one-live-row-per-name index is built CONCURRENTLY after migrations
-- (EnsureLiveArtifactIndex, live_artifact_index.go), not here: session_artifacts holds a growing
-- session's published files and can be large, and CREATE UNIQUE INDEX inside this migration's
-- transaction would hold its writes for the whole build. Until that index exists, two concurrent
-- first-time `--live` publishes of a brand new name could in principle both insert a live row for
-- it; EnsureLiveArtifactIndex starts as soon as the server is up, so the window is brief.
