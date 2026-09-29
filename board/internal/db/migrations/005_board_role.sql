-- Ephemeral board-level sessions: not tied to a card, spawned from the board
-- header to talk, triage, and create cards.
ALTER TABLE runner_sessions DROP CONSTRAINT runner_sessions_role_check;
ALTER TABLE runner_sessions ADD CONSTRAINT runner_sessions_role_check
    CHECK (role IN ('worker', 'reviewer', 'discuss', 'board'));
