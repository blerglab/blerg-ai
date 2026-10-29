-- Space unknown-reset meter requests across server restarts and replicas.
ALTER TABLE session_limit_resume ADD COLUMN next_meter_poll_at timestamptz;
