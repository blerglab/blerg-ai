-- sessions.started_by_kind / started_by_name: who started the session, so the person's own
-- sessions and the ones a tool, a cron or the operator key started can be told apart in the
-- app. kind is the start's principal kind ("agent", "runner_key", "cron"; "" = the person, in
-- the app) and name the owner's label for the agent token, when it was one. Back-filled from
-- what the row already knew: a cron id, or an agent token id.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS started_by_kind text NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS started_by_name text NOT NULL DEFAULT '';
UPDATE sessions SET started_by_kind = 'cron' WHERE started_by_kind = '' AND cron_id IS NOT NULL;
UPDATE sessions SET started_by_kind = 'agent' WHERE started_by_kind = '' AND token_id IS NOT NULL;
