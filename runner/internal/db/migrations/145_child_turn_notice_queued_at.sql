-- BLG-447 review: childTurnFinishedReadiness held a 'finished' notice (open background work, or
-- within its grace window) by using the daemon-reported turn_done event's own ts, which clamps to
-- within 10 minutes of the server's clock either way (agentevents.go's eventTimeSkew) but still
-- lets a fast daemon clock delay delivery. queued_at is this row's OWN server-side timestamp,
-- taken from the trigger's now() in the same transaction as the server's insert — never a daemon's
-- reported time — so the grace and ceiling windows measure against the server's own receipt of
-- the event, not the daemon's clock. It resets every time the row's content is refreshed (the
-- UPDATE branch below), since a replaced report earns its own fresh grace window.
ALTER TABLE child_turn_notices ADD COLUMN queued_at timestamptz NOT NULL DEFAULT now();

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
  UPDATE child_turn_notices SET event_seq=NEW.seq, queued_at=now(),
   kind=CASE WHEN NEW.payload->>'stop_reason'='error' THEN 'failed' ELSE 'finished' END,delivered=false
   WHERE child_id=NEW.session_id AND turn_seq=pending;
 ELSE
  INSERT INTO child_turn_notices(child_id,turn_seq,event_seq,kind,queued_at)
  VALUES(NEW.session_id,anchor,NEW.seq,CASE WHEN NEW.payload->>'stop_reason'='error' THEN 'failed' ELSE 'finished' END,now())
  ON CONFLICT DO NOTHING;
 END IF;
 RETURN NEW;
END;
$$;
