CREATE TABLE signing_keys (
    kid         text PRIMARY KEY,
    public_key  bytea NOT NULL,
    private_key bytea NOT NULL,
    active      boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE revocations (
    kind       text NOT NULL,          -- 'kid' | 'lineage' | 'sub'
    value      text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, value)
);
