-- Cluster-private storage and immutable Git destination contexts (no tokens).
-- Retain the binding after session deletion so uncertain local work is not GC'd.
CREATE TABLE cluster_workspace_storage (
 session_id uuid PRIMARY KEY,
 launch_generation text NOT NULL,
 owner_id text NOT NULL,
 claim_name text NOT NULL,
 claim_uid text NOT NULL,
 member_identities jsonb,
 reported_generation bigint NOT NULL DEFAULT 0,
 report_revision bigint NOT NULL DEFAULT 0,
 contexts jsonb NOT NULL CHECK (jsonb_typeof(contexts)='array' AND NOT jsonb_path_exists(contexts,'$[*].token'))
);
