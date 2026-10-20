-- Idempotent on purpose (as are 015 to 020): core applies a migration and then records it in a
-- separate statement, so a crash in between re-runs the file on the next start.
-- First-run setup (core/internal/authprovider/setup.go): while no account exists, the first admin
-- is created on /setup with a single-use setup code. At most one code is live: this table holds
-- one row (id is always true), and minting a code replaces it. Only the SHA-256 of the code is
-- stored; creating the first account deletes the row.
CREATE TABLE IF NOT EXISTS setup_code (
    id          boolean PRIMARY KEY DEFAULT true CHECK (id),
    code_hash   text NOT NULL,
    expires_at  timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
