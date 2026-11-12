-- Draft revisions survive replacement and consumption; an ask never follows a moving draft.
ALTER TABLE blackboard_drafts ADD COLUMN id uuid NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE blackboard_drafts ADD COLUMN revision integer NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX blackboard_drafts_id ON blackboard_drafts(id);
CREATE TABLE blackboard_draft_revisions (
 id uuid NOT NULL,
 revision integer NOT NULL,
 workspace_id text NOT NULL,
 draft jsonb NOT NULL,
 created_by text NOT NULL,
 created_at timestamptz NOT NULL,
 PRIMARY KEY(id,revision)
);
INSERT INTO blackboard_draft_revisions SELECT id,revision,workspace_id,draft,created_by,created_at FROM blackboard_drafts;
CREATE FUNCTION blackboard_draft_revision_save() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP IN ('UPDATE','DELETE') THEN
  WITH canceled AS (
   UPDATE bound_asks SET status='canceled',note='stale_draft',decision=jsonb_build_object('status','canceled','cause','stale_draft')
   WHERE status='open' AND subject->>'kind'='draft' AND subject->>'draft_id'=OLD.id::text RETURNING message_id
  ) UPDATE messages SET status='answered',answer='',answered_by='server',answered_at=now(),no_wait=true WHERE id IN (SELECT message_id FROM canceled);
 END IF;
 IF TG_OP <> 'DELETE' THEN
  INSERT INTO blackboard_draft_revisions VALUES(NEW.id,NEW.revision,NEW.workspace_id,NEW.draft,NEW.created_by,NEW.created_at);
  RETURN NEW;
 END IF;
 RETURN OLD;
END $$;
CREATE TRIGGER blackboard_draft_revision_save AFTER INSERT OR UPDATE OR DELETE ON blackboard_drafts FOR EACH ROW EXECUTE FUNCTION blackboard_draft_revision_save();
CREATE FUNCTION blackboard_draft_revision_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'draft revision is immutable'; END $$;
CREATE TRIGGER blackboard_draft_revision_immutable BEFORE UPDATE OR DELETE ON blackboard_draft_revisions FOR EACH ROW EXECUTE FUNCTION blackboard_draft_revision_immutable();
-- A draft ask exists before its blackboard. The immutable subject carries its draft identity.
ALTER TABLE bound_asks ALTER COLUMN blackboard_id DROP NOT NULL;
ALTER TABLE bound_asks ADD COLUMN result_id text;
CREATE OR REPLACE FUNCTION bound_ask_subject_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.session_epoch<>OLD.session_epoch OR NEW.subject IS DISTINCT FROM OLD.subject OR NEW.owner_id<>OLD.owner_id OR NEW.workspace_id<>OLD.workspace_id OR NEW.blackboard_id IS DISTINCT FROM OLD.blackboard_id THEN
  RAISE EXCEPTION 'bound subject is immutable';
 END IF;
 RETURN NEW;
END $$;
-- Session ownership changes invalidate the durable epoch in migration 147.
-- Cancellation delivery runs after commit, never in a session row trigger.
