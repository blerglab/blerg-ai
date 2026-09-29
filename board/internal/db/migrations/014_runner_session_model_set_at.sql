-- When blerg-board last set this session's model itself. Runner-reported model
-- changes are followed only when they are NEWER than this: an event still
-- sitting in the ingest backlog reports the model from before blerg-board's own
-- change, and applying it would undo the change and stay undone until the
-- runner happened to report again. NULL = blerg-board never set it (spawn only).
ALTER TABLE runner_sessions ADD COLUMN model_set_at timestamptz;
