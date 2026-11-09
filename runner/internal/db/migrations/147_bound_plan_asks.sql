-- A bound ask is a decision, never an engine input. Subject and ownership are
-- immutable; consumption and the corresponding plan revision share a transaction.
CREATE TABLE bound_asks (
 message_id uuid PRIMARY KEY REFERENCES messages(id),
 workspace_id uuid NOT NULL REFERENCES sessions(id),
 blackboard_id text NOT NULL REFERENCES blackboards(id),
 owner_id text NOT NULL,
 subject jsonb NOT NULL,
 status text NOT NULL DEFAULT 'open' CHECK(status IN ('open','committed','declined','canceled')),
 expires_at timestamptz NOT NULL DEFAULT now()+interval '24 hours',
 decision jsonb,
 decision_input_id text,
 note text NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX bound_asks_one_open ON bound_asks(workspace_id) WHERE status='open';
CREATE UNIQUE INDEX bound_asks_input_once ON bound_asks(workspace_id,decision_input_id) WHERE decision_input_id IS NOT NULL;
CREATE FUNCTION bound_ask_close() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status <> 'open' AND EXISTS(SELECT 1 FROM bound_asks WHERE message_id=NEW.id AND status='open') THEN
  UPDATE bound_asks SET status='canceled',note='Question closed without a decision.' WHERE message_id=NEW.id AND status='open';
  NEW.answer='';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER bound_ask_close BEFORE UPDATE OF status ON messages FOR EACH ROW EXECUTE FUNCTION bound_ask_close();
-- Session lifecycle writes must never acquire question/binding locks. A durable
-- epoch invalidates old asks even if Main resumes or ownership transfers back.
ALTER TABLE sessions ADD COLUMN bound_ask_epoch bigint NOT NULL DEFAULT 0;
ALTER TABLE bound_asks ADD COLUMN session_epoch bigint NOT NULL DEFAULT 0;
CREATE FUNCTION bound_ask_session_end() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status IN ('ended','stopped') OR NEW.ended_at IS NOT NULL OR NEW.archived_at IS NOT NULL OR NEW.discard_requested_at IS NOT NULL
    OR NEW.spawning_account_id IS DISTINCT FROM OLD.spawning_account_id THEN
  NEW.bound_ask_epoch=OLD.bound_ask_epoch+1;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER bound_ask_session_end BEFORE UPDATE OF status,ended_at,archived_at,discard_requested_at,spawning_account_id ON sessions FOR EACH ROW EXECUTE FUNCTION bound_ask_session_end();
CREATE FUNCTION bound_ask_owner_changed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.owner_id IS DISTINCT FROM OLD.owner_id THEN
  UPDATE messages SET status='answered',answer='',answered_at=now() WHERE id IN (SELECT message_id FROM bound_asks WHERE blackboard_id=NEW.id AND status='open');
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER bound_ask_owner_changed AFTER UPDATE OF owner_id ON blackboards FOR EACH ROW EXECUTE FUNCTION bound_ask_owner_changed();
CREATE FUNCTION bound_ask_subject_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.session_epoch<>OLD.session_epoch OR NEW.subject IS DISTINCT FROM OLD.subject OR NEW.owner_id<>OLD.owner_id OR NEW.workspace_id<>OLD.workspace_id OR NEW.blackboard_id<>OLD.blackboard_id THEN
  RAISE EXCEPTION 'bound subject is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER bound_ask_subject_immutable BEFORE UPDATE ON bound_asks FOR EACH ROW EXECUTE FUNCTION bound_ask_subject_immutable();

CREATE INDEX bound_ask_notes_latest ON agent_events(session_id,(payload->>'message_id'),seq DESC) WHERE kind='bound_ask_note';
