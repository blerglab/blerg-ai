-- Failure and restart reports are conversation notices, never questions.
-- Keep their delivery/recovery lifecycle separate from person-facing questions.
-- An old runner keeps writing during a rolling deploy; hold its appends while seq is assigned.
LOCK TABLE agent_events IN SHARE ROW EXCLUSIVE MODE;
ALTER TABLE session_failure_alerts ADD COLUMN notice_event_id uuid NOT NULL DEFAULT gen_random_uuid();
UPDATE messages m SET kind='update', owner_only=false, no_wait=false,
 status=CASE WHEN m.status='open' THEN 'answered' ELSE m.status END,
 body=replace(replace(body, ' Check its work, then dismiss this alert.', ' Check its work.'),
 ' Check its work before resuming, then dismiss this alert.', ' Check its work before resuming.')
FROM session_failure_alerts f WHERE f.message_id=m.id;
-- Old alerts were only in Questions. Preserve them in the actual conversation.
INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload,ts)
SELECT f.session_id,
 COALESCE((SELECT max(e.seq) FROM agent_events e WHERE e.session_id=f.session_id),0)
 + row_number() OVER (PARTITION BY f.session_id ORDER BY m.created_at,m.id),
 f.notice_event_id,'update',jsonb_build_object('body',m.body,'message_id',m.id),m.created_at
FROM session_failure_alerts f JOIN messages m ON m.id=f.message_id
ON CONFLICT(session_id,client_event_id) DO NOTHING;
