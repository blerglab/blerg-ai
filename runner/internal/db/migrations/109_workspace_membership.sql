-- Live repository membership (BLG-321). session_workspaces.manifest stays the
-- immutable launch selection; the pointer selects the current authorized revision.
ALTER TABLE session_workspaces ADD COLUMN membership_revision bigint NOT NULL DEFAULT 0;

CREATE TABLE workspace_membership_operations (
 id uuid PRIMARY KEY,
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 kind text NOT NULL CHECK (kind IN ('add','remove')),
 phase text NOT NULL CHECK (phase IN ('requested','authorized','committed','installed','denied','refused')),
 requested_by text NOT NULL CHECK (requested_by IN ('person','main')),
 principal text NOT NULL,
 approved_by text NOT NULL DEFAULT '',
 expected_revision bigint NOT NULL,
 old_digest text NOT NULL,
 proposed_digest text NOT NULL,
 proposed jsonb NOT NULL,
 member_id text NOT NULL,
 member_name text NOT NULL,
 source jsonb NOT NULL DEFAULT '{}',
 request_digest text NOT NULL,
 receipt text NOT NULL DEFAULT '',
 waiting text NOT NULL DEFAULT '',
 refusal text NOT NULL DEFAULT '',
 -- Ignored files a refused removal names, and the digest a person's later
 -- confirmation binds so exactly those files may be deleted with the checkout.
 local_files jsonb NOT NULL DEFAULT '{}',
 discard_local text NOT NULL DEFAULT '',
 committed_revision bigint,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
-- One change at a time per workspace, including one committed but not installed.
CREATE UNIQUE INDEX workspace_membership_one_open ON workspace_membership_operations(session_id) WHERE phase IN ('requested','authorized','committed');

CREATE TABLE session_workspace_revisions (
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 revision bigint NOT NULL CHECK (revision > 0),
 manifest jsonb NOT NULL,
 operation_id uuid NOT NULL REFERENCES workspace_membership_operations(id),
 retired_results jsonb NOT NULL DEFAULT '[]',
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(session_id, revision)
);

CREATE FUNCTION session_workspace_revisions_append_only() RETURNS trigger AS $$
BEGIN
 RAISE EXCEPTION 'workspace revisions are append-only';
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER session_workspace_revisions_no_update BEFORE UPDATE ON session_workspace_revisions
 FOR EACH ROW EXECUTE FUNCTION session_workspace_revisions_append_only();
