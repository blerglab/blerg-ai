-- 016: sessions.status_changed_at.
--
-- Several of the runner's timeouts are about how long a session has been in
-- its CURRENT state — a disconnected cluster session nobody resumes, a session
-- whose Job cannot be found — and started_at answers a different question
-- entirely: a session that ran for a day and disconnected a minute ago is not
-- stale. UpdateSessionStatus stamps this on every transition; rows written
-- before this migration have NULL and fall back to started_at.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS status_changed_at timestamptz;
