-- One outstanding completion per child. Keep its receipt identity while updating
-- the report, including after acceptance into an unconsumed parent input outbox.
CREATE OR REPLACE FUNCTION queue_child_turn_notice() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE anchor bigint; pending bigint;
BEGIN
 IF NEW.payload->>'stop_reason' = 'interrupted'
    OR NOT EXISTS(SELECT 1 FROM sessions WHERE id=NEW.session_id AND parent_session_id IS NOT NULL) THEN
  RETURN NEW;
 END IF;
 SELECT COALESCE(max(seq),0) INTO anchor FROM agent_events
 WHERE session_id=NEW.session_id AND seq<NEW.seq AND
  (kind='user_message' OR (kind IN ('assistant_text','tool_call') AND COALESCE(payload->>'parent_call_id','')=''));
 SELECT c.turn_seq INTO pending FROM child_turn_notices c JOIN sessions s ON s.id=c.child_id
 WHERE c.child_id=NEW.session_id AND (NOT c.delivered OR EXISTS(
  SELECT 1 FROM session_inputs i WHERE i.session_id=s.parent_session_id
  AND i.client_id='child-turn-'||c.child_id::text||'-finished-'||c.turn_seq::text AND i.settled_at IS NULL))
 ORDER BY c.event_seq DESC LIMIT 1 FOR UPDATE OF c;
 IF FOUND THEN
  UPDATE child_turn_notices SET event_seq=NEW.seq,
   kind=CASE WHEN NEW.payload->>'stop_reason'='error' THEN 'failed' ELSE 'finished' END,delivered=false
   WHERE child_id=NEW.session_id AND turn_seq=pending;
 ELSE
  INSERT INTO child_turn_notices(child_id,turn_seq,event_seq,kind)
  VALUES(NEW.session_id,anchor,NEW.seq,CASE WHEN NEW.payload->>'stop_reason'='error' THEN 'failed' ELSE 'finished' END)
  ON CONFLICT DO NOTHING;
 END IF;
 RETURN NEW;
END;
$$;
-- Supersede older reserved completion inputs already accepted by the previous
-- server. Preserve their receipts, but never replay stale wakes after upgrade.
UPDATE session_inputs i SET settled_at=now()
FROM child_turn_notices c JOIN sessions s ON s.id=c.child_id
WHERE i.session_id=s.parent_session_id AND i.settled_at IS NULL
 AND i.frame->>'source'='child_turn'
 AND i.client_id='child-turn-'||c.child_id::text||'-finished-'||c.turn_seq::text
 AND EXISTS(SELECT 1 FROM child_turn_notices newer
  WHERE newer.child_id=c.child_id AND newer.event_seq>c.event_seq);
-- The latest completion supersedes earlier undelivered intents too, even if
-- its own input was accepted before the restart.
UPDATE child_turn_notices c SET delivered=true WHERE NOT delivered AND EXISTS
 (SELECT 1 FROM child_turn_notices newer WHERE newer.child_id=c.child_id AND newer.event_seq>c.event_seq);
