-- Snapshot metadata and linkage before workspace cleanup. The trigger runs in
-- the same transaction as accepted results; no board call is made here.
CREATE TABLE session_commit_deliveries (
 id bigserial PRIMARY KEY,
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 digest text NOT NULL,
 manifest jsonb NOT NULL,
 results jsonb NOT NULL,
 title text NOT NULL,
 board_id text,
 ticket_through bigint NOT NULL,
 runtime_generation bigint NOT NULL,
 completed_at timestamptz,
 attempts integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 failed boolean NOT NULL DEFAULT false,
 UNIQUE(session_id,digest)
);
CREATE INDEX session_commit_deliveries_pending ON session_commit_deliveries(id) WHERE completed_at IS NULL;
CREATE FUNCTION capture_workspace_delivery() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF jsonb_path_exists(NEW.results,'$[*].summary.commits[0]') THEN
  INSERT INTO session_commit_deliveries(session_id,digest,manifest,results,title,board_id,ticket_through,runtime_generation)
  SELECT NEW.session_id,md5(NEW.results::text),COALESCE(r.manifest,NEW.manifest),NEW.results,COALESCE(s.title,''),COALESCE(s.board_id::text,
   (SELECT min(board) FROM (
     SELECT builtin_ref AS board FROM session_mcp_grants WHERE session_id=s.id AND builtin='board' AND builtin_ref<>''
     UNION SELECT board_id FROM session_proposal_grants WHERE session_id=s.id AND kind='board_file' AND revoked_at IS NULL AND level<>'propose'
     UNION SELECT board_id FROM session_card_links WHERE session_id=s.id AND source='pin'
    ) contexts HAVING count(*)=1)),
   COALESCE((SELECT max(seq) FROM agent_events WHERE session_id=NEW.session_id),0),NEW.runtime_generation
  FROM sessions s LEFT JOIN session_workspace_revisions r ON r.session_id=s.id AND r.revision=NEW.membership_revision
  WHERE s.id=NEW.session_id ON CONFLICT DO NOTHING;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER session_workspace_commit_delivery AFTER INSERT OR UPDATE OF results ON session_workspaces
 FOR EACH ROW EXECUTE FUNCTION capture_workspace_delivery();
