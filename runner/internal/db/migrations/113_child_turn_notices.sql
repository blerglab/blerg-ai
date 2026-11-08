-- The transcript and its notification intent commit together. Replayed events
-- cannot duplicate intent, and a server restart cannot lose an acknowledged end.
CREATE TABLE child_turn_notices (
 child_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 turn_seq bigint NOT NULL,
 event_seq bigint NOT NULL,
 kind text NOT NULL,
 delivered boolean NOT NULL DEFAULT false,
 PRIMARY KEY(child_id, turn_seq)
);
CREATE INDEX child_turn_notices_pending ON child_turn_notices(child_id, event_seq) WHERE NOT delivered;

CREATE FUNCTION queue_child_turn_notice() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE anchor bigint;
BEGIN
 IF NEW.kind <> 'turn_done' OR NEW.payload->>'stop_reason' = 'interrupted'
    OR NOT EXISTS(SELECT 1 FROM sessions WHERE id=NEW.session_id AND parent_session_id IS NOT NULL) THEN
  RETURN NEW;
 END IF;
 -- A harness may continue after background work without another user input.
 -- Completed root text/tool work distinguishes that report from duplicate ends.
 SELECT COALESCE(max(seq),0) INTO anchor FROM agent_events
 WHERE session_id=NEW.session_id AND seq<NEW.seq AND
  (kind='user_message' OR (kind IN ('assistant_text','tool_call') AND COALESCE(payload->>'parent_call_id','')=''));
 INSERT INTO child_turn_notices(child_id,turn_seq,event_seq,kind)
 VALUES(NEW.session_id,anchor,NEW.seq,CASE WHEN NEW.payload->>'stop_reason'='error' THEN 'failed' ELSE 'finished' END)
 ON CONFLICT DO NOTHING;
 RETURN NEW;
END;
$$;
CREATE TRIGGER agent_event_child_turn_notice AFTER INSERT ON agent_events
FOR EACH ROW WHEN (NEW.kind='turn_done') EXECUTE FUNCTION queue_child_turn_notice();
