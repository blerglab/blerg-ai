CREATE TABLE components (
    name             text PRIMARY KEY,
    base_url         text NOT NULL,
    version          text NOT NULL,
    contract_version text NOT NULL,
    capabilities     jsonb NOT NULL DEFAULT '[]',
    last_seen        timestamptz NOT NULL DEFAULT now()
);
