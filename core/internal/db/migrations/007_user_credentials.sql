CREATE TABLE user_credentials (
    account_id  uuid NOT NULL REFERENCES accounts(id),
    engine      text NOT NULL,
    ciphertext  bytea NOT NULL,
    key_id      text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, engine)
);
