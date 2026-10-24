-- Private desktop-runtime transaction journal. Never serialized to browsers.
CREATE TABLE local_workspace_authority (
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 token text NOT NULL,
 launch_generation text NOT NULL,
 runtime_generation bigint NOT NULL CHECK (runtime_generation > 0),
 connection_generation text NOT NULL,
 aborted boolean NOT NULL DEFAULT false,
 final_digest text,
 final_outcome jsonb,
 PRIMARY KEY(session_id, token),
 CHECK ((final_digest IS NULL) = (final_outcome IS NULL))
);
CREATE UNIQUE INDEX local_workspace_one_active ON local_workspace_authority(session_id) WHERE NOT aborted;
