-- Retain the refused window and observation time across reconnects and restarts.
ALTER TABLE session_limit_resume
 ADD COLUMN window_id text NOT NULL DEFAULT '',
 ADD COLUMN refused_at timestamptz;
