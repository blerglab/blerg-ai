-- 066: child sessions on the cluster. A parent running in a cluster pod starts children that run
-- as cluster pods, so a standing rule or a session grant can name the cluster runtime (its daemon
-- is the server's synthetic cluster daemon).
ALTER TABLE delegation_grants DROP CONSTRAINT IF EXISTS delegation_grants_runtime_check;
ALTER TABLE delegation_grants ADD CONSTRAINT delegation_grants_runtime_check
  CHECK (runtime IN ('docker','daemon','cluster'));
ALTER TABLE delegation_rules DROP CONSTRAINT IF EXISTS delegation_rules_runtime_check;
ALTER TABLE delegation_rules ADD CONSTRAINT delegation_rules_runtime_check
  CHECK (runtime IN ('docker','daemon','cluster'));

-- The browser session (sid) of the owner's latest launch or resume of a cluster session, or of
-- their latest approval of a child it asked for. A child that session starts on the cluster under a
-- rule or grant is authorized with it (core still checks that it is live and the owner's), as a
-- resume is with the proof of its request. NULL for every other session.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS launch_auth_session_id text;
