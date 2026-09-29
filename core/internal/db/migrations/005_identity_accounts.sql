CREATE TABLE accounts (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider              text NOT NULL,           -- "local" | "github" | "oidc"
    provider_subject      text NOT NULL,            -- password: account id as string; github/oidc: their sub
    email                 text,
    role                  text NOT NULL DEFAULT 'member',
    must_change_password  boolean NOT NULL DEFAULT false,
    password_hash         text,                     -- bcrypt, "local" provider only
    created_at            timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, provider_subject)
);

CREATE TABLE human_sessions (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id         uuid NOT NULL REFERENCES accounts(id),
    token_hash         text NOT NULL UNIQUE,
    created_at         timestamptz NOT NULL DEFAULT now(),
    last_used_at       timestamptz,
    revoked_at         timestamptz,
    user_agent         text,
    ip                 text
);
CREATE INDEX human_sessions_account_id_idx ON human_sessions(account_id);

CREATE TABLE credential_access_log (
    id                     bigserial PRIMARY KEY,
    account_id             uuid NOT NULL,
    engine                 text NOT NULL,
    fetched_by_session_id  uuid,
    fetched_at             timestamptz NOT NULL DEFAULT now()
);
