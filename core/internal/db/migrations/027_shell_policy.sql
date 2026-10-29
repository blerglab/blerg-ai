-- Install-wide switch. Idempotent because core records migration completion separately.
CREATE TABLE IF NOT EXISTS shell_policy (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    revision bigint NOT NULL CHECK (revision > 0),
    enabled boolean NOT NULL,
    updated_by uuid,
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO shell_policy(singleton, revision, enabled) VALUES(true, 1, true)
ON CONFLICT (singleton) DO NOTHING;

-- Commit-time wakeups carry no account data. Readers re-read authoritative state.
-- PostgreSQL delivers NOTIFY only after commit, including for identity changes made by
-- another core replica or a transaction that combines a password change and revocation.
CREATE OR REPLACE FUNCTION notify_shell_authority() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify('blerg_shell_authority', '');
    RETURN NULL;
END;
$$;
DROP TRIGGER IF EXISTS shell_policy_changed ON shell_policy;
CREATE TRIGGER shell_policy_changed AFTER INSERT OR UPDATE OR DELETE ON shell_policy
FOR EACH STATEMENT EXECUTE FUNCTION notify_shell_authority();
DROP TRIGGER IF EXISTS shell_login_changed ON human_sessions;
CREATE TRIGGER shell_login_changed AFTER INSERT OR UPDATE OR DELETE ON human_sessions
FOR EACH STATEMENT EXECUTE FUNCTION notify_shell_authority();
DROP TRIGGER IF EXISTS shell_revocation_changed ON revocations;
CREATE TRIGGER shell_revocation_changed AFTER INSERT OR UPDATE OR DELETE ON revocations
FOR EACH STATEMENT EXECUTE FUNCTION notify_shell_authority();
DROP TRIGGER IF EXISTS shell_account_changed ON accounts;
CREATE TRIGGER shell_account_changed AFTER UPDATE OR DELETE ON accounts
FOR EACH STATEMENT EXECUTE FUNCTION notify_shell_authority();
