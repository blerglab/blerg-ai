-- An administrator can raise one session's attachment total above the install default (#231).
-- NULL keeps the install default; every raise is recorded in session_storage_audit.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS upload_limit_bytes bigint CHECK (upload_limit_bytes > 0);
CREATE TABLE IF NOT EXISTS session_storage_audit (
    id bigserial PRIMARY KEY,
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    actor text NOT NULL,
    previous_bytes bigint NOT NULL,
    new_bytes bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS session_storage_audit_session ON session_storage_audit(session_id);
-- Who deleted which session file (#231): the person decided for, how the request came (browser,
-- an agent/human token on the runner contract, or a broker such as the board's runner key naming
-- the person), and what the file was. Kept after the file and, like its siblings, until the session goes.
CREATE TABLE IF NOT EXISTS session_file_deletions (
    id bigserial PRIMARY KEY,
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    artifact_id text NOT NULL,
    name text NOT NULL,
    origin text NOT NULL,
    version integer NOT NULL,
    size bigint NOT NULL,
    actor text NOT NULL,
    actor_admin boolean NOT NULL,
    via text NOT NULL,
    deleted_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS session_file_deletions_session ON session_file_deletions(session_id);
