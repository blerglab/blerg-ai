-- Existing accounts predate What's New; new accounts baseline to the release
-- registered by core before it accepts sign-ins or account creation.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = 'accounts'
          AND column_name = 'whats_new_seen'
    ) THEN
        ALTER TABLE accounts ADD COLUMN whats_new_seen text;
        UPDATE accounts SET whats_new_seen = '0.5.4';
    END IF;
END;
$$;
CREATE TABLE IF NOT EXISTS app_release_baseline (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    version text NOT NULL
);
CREATE OR REPLACE FUNCTION baseline_account_release() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.whats_new_seen := (SELECT version FROM app_release_baseline WHERE singleton);
    RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER accounts_release_baseline BEFORE INSERT ON accounts
FOR EACH ROW EXECUTE FUNCTION baseline_account_release();

-- Account creation can precede the first sign-in by several upgrades. Baseline
-- that first sign-in, while retaining unread notes on every later sign-in.
CREATE OR REPLACE FUNCTION baseline_first_account_login() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM 1 FROM accounts WHERE id = NEW.account_id FOR UPDATE;
    IF NOT EXISTS (SELECT 1 FROM human_sessions WHERE account_id = NEW.account_id) THEN
        UPDATE accounts SET whats_new_seen = (
            SELECT version FROM app_release_baseline WHERE singleton
        ) WHERE id = NEW.account_id;
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER human_sessions_release_baseline BEFORE INSERT ON human_sessions
FOR EACH ROW EXECUTE FUNCTION baseline_first_account_login();
