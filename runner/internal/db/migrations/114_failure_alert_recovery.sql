-- Recovery belongs to the failure event, including alerts the person dismissed.
ALTER TABLE session_failure_alerts ADD COLUMN recovered_at timestamptz;
ALTER TABLE session_failure_alerts ADD COLUMN quiet boolean NOT NULL DEFAULT false;
-- Withdrawn alerts from the previous resolver already represent recovery.
UPDATE session_failure_alerts f SET recovered_at=now() FROM messages m WHERE m.id=f.message_id AND m.status='withdrawn';
