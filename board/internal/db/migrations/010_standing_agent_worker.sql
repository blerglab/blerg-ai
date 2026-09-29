-- Standing column agents: sessions the standing-agent worker spawns get their
-- own role, and persistent-mode sessions (card_id NULL — they process many
-- cards) are tracked back to their agent by this column instead.
ALTER TABLE runner_sessions DROP CONSTRAINT runner_sessions_role_check;
ALTER TABLE runner_sessions ADD CONSTRAINT runner_sessions_role_check
    CHECK (role IN ('worker', 'reviewer', 'discuss', 'board', 'standing'));

ALTER TABLE runner_sessions ADD COLUMN standing_agent_id uuid
    REFERENCES standing_agents(id) ON DELETE SET NULL;
CREATE INDEX runner_sessions_standing_agent_idx ON runner_sessions (standing_agent_id)
    WHERE standing_agent_id IS NOT NULL;
