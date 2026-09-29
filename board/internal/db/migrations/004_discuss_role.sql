-- Sessions can also be conversational: a "discuss" session talks about the
-- card with the human without executing it or moving it.
ALTER TABLE runner_sessions DROP CONSTRAINT runner_sessions_role_check;
ALTER TABLE runner_sessions ADD CONSTRAINT runner_sessions_role_check
    CHECK (role IN ('worker', 'reviewer', 'discuss'));
