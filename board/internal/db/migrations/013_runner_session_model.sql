-- The model a runner session is actually running on. Set at spawn from the
-- resolved card/board/role precedence, and updated when the session is
-- re-modelled mid-flight (a session escalating itself, or a human doing it
-- for it). Empty = unknown: spawned before this column existed, or the board
-- left the model unset and the runner picked its own default.
ALTER TABLE runner_sessions ADD COLUMN model text NOT NULL DEFAULT '';
