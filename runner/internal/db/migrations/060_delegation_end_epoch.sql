-- A resumed parent's current state cannot erase an earlier delegation boundary.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS delegation_ended_at timestamptz;

UPDATE sessions SET delegation_ended_at = ended_at
WHERE ended_at IS NOT NULL AND (end_reason IN
 ('stopped_by_user','stopped_by_agent','auto_stopped','process_exited',
  'process_failed','start_failed','parent_ended')
 OR (end_reason='daemon_lost' AND status='stopped'));
