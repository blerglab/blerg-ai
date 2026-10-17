-- Per-account preferences the person sets in the app and wants on every browser: one JSON value per
-- (account, key). The first key is "code_colours" (the syntax-colour theme per mode and its token
-- overrides; GET/PUT /api/me/preferences). The account is the signed-in person's core account id, not
-- a foreign key: accounts live in blerg-core.
CREATE TABLE IF NOT EXISTS account_preferences (
    account_id text NOT NULL,
    key        text NOT NULL,
    value      jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, key)
);
