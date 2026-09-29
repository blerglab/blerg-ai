CREATE TABLE encryption_keys (
    key_id      text PRIMARY KEY,
    key         bytea NOT NULL,
    active      boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now()
);
