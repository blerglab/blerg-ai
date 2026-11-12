-- Child questions first belong to their coordinator. Escalation preserves the
-- original question ID so an answer racing escalation has a single winner.
ALTER TABLE messages ADD COLUMN child_escalated boolean NOT NULL DEFAULT false;
ALTER TABLE messages ADD COLUMN child_parent_notified boolean NOT NULL DEFAULT false;
ALTER TABLE messages ADD COLUMN child_person_notified boolean NOT NULL DEFAULT false;

ALTER TABLE messages ADD COLUMN person_required boolean NOT NULL DEFAULT false;
ALTER TABLE messages ADD COLUMN answered_by text NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN escalated_by text NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN escalation_reason text NOT NULL DEFAULT '';
-- The blocked caller went away. The question stays with the person; its answer
-- reaches the session as a message.
ALTER TABLE messages ADD COLUMN waiter_ended boolean NOT NULL DEFAULT false;
-- Which hosting of the session asked. A daemon mints a higher value before each engine
-- start, and recovery reaches only lower ones, however late its retry lands.
ALTER TABLE messages ADD COLUMN host_generation bigint NOT NULL DEFAULT 0;

-- Before this migration child asks were already shown and pushed to the person.
-- Keep that ownership without emitting another escalation or notification.
UPDATE messages m SET child_escalated=true, child_person_notified=true,
 escalated_by='system', escalation_reason='upgrade',
 person_required=(m.owner_only AND NOT m.no_wait)
FROM sessions s WHERE s.id=m.session_id AND s.parent_session_id IS NOT NULL
 AND m.kind='ask' AND m.status='open'
 AND NOT EXISTS(SELECT 1 FROM session_failure_alerts f WHERE f.message_id=m.id);
