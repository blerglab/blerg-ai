-- An unowned endpoint cannot safely receive any account's notifications.
DELETE FROM push_subscriptions WHERE account_id = '';
-- Bind delivery to a live login, so sign-out stops push even with the runner closed.
-- Existing owned rows fail closed until their browser silently re-registers.
ALTER TABLE push_subscriptions ADD COLUMN session_id text NOT NULL DEFAULT '';
CREATE INDEX push_subscriptions_account_idx ON push_subscriptions(account_id);
