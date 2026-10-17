-- Models and effort are selected together, independently for each board role.
ALTER TABLE boards ADD COLUMN effort text NOT NULL DEFAULT '',
 ADD COLUMN reviewer_effort text NOT NULL DEFAULT '',
 ADD COLUMN discuss_effort text NOT NULL DEFAULT '',
 ADD COLUMN chat_effort text NOT NULL DEFAULT '';
ALTER TABLE cards ADD COLUMN effort text NOT NULL DEFAULT '';
ALTER TABLE runner_sessions ADD COLUMN engine text NOT NULL DEFAULT '',
 ADD COLUMN effort text NOT NULL DEFAULT '',
 ADD COLUMN resume_mode text NOT NULL DEFAULT '',
 ADD COLUMN resume_reason text NOT NULL DEFAULT '';
-- Implemented engines are validated by the shared registry. Installation and
-- credential availability are resolved by scoped runner discovery, not SQL.
ALTER TABLE boards DROP CONSTRAINT IF EXISTS boards_automation_engine_check;
