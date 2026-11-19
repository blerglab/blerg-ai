-- One answer per exact published HTML version; no credentials or executable result.
CREATE TABLE answer_requests (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 artifact_id text NOT NULL UNIQUE,
 artifact_name text NOT NULL,
 artifact_version integer NOT NULL,
 ask text NOT NULL CHECK (octet_length(ask) <= 4000),
 schema json NOT NULL CHECK (octet_length(schema::text) <= 8192),
 result json,
 answered_by text,
 delivered boolean NOT NULL DEFAULT false,
 superseded boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK (result IS NULL OR octet_length(result::text) <= 16384)
);
CREATE INDEX answer_requests_session_idx ON answer_requests(session_id);

-- Supersession is permanent, including if the newer file is later deleted. This runs
-- within artifact insertion's transaction and advisory lock, so request creation and
-- submission cannot race past a new version without seeing the closed state.
CREATE FUNCTION supersede_answer_requests() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.origin = 'agent' THEN
  UPDATE answer_requests SET superseded=true
   WHERE session_id=NEW.session_id AND artifact_name=NEW.name
    AND artifact_version < NEW.version AND NOT delivered AND NOT superseded;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER artifact_supersedes_answers AFTER INSERT ON session_artifacts
 FOR EACH ROW EXECUTE FUNCTION supersede_answer_requests();
