-- 011_session_spawning_account.sql: record the verified account id
-- (identity.Principal.Sub) that spawned a cluster-runtime session, so a
-- resume (resumeClusterSession) can look up the same account's personal
-- credential instead of silently falling back to the shared operator
-- Secret.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS spawning_account_id text;
