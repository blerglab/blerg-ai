-- Additive: old scalar readers/writers retain their existing meanings.
ALTER TABLE sessions ADD COLUMN workspace_kind text NOT NULL DEFAULT 'repo'
 CHECK (workspace_kind IN ('repo', 'folder'));
CREATE TABLE session_workspaces (
 session_id uuid PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
 launch_generation text NOT NULL CHECK (launch_generation <> ''),
 manifest jsonb NOT NULL CHECK (manifest->>'version' IS NOT NULL AND manifest->>'version' = '2'),
 results jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(results) = 'array')
);
-- Immutable manifests are inserted once; only generation-fenced results change.
