-- Existing subscriptions remain unowned until the browser registers again.
ALTER TABLE push_subscriptions ADD COLUMN account_id text NOT NULL DEFAULT '';
