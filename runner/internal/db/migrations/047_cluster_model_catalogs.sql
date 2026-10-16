-- Account/image-scoped catalogs survive server restarts. Only the sanitized model
-- catalog is stored here; credentials and the authorization proof never are.
CREATE TABLE cluster_model_catalogs (
 account_id text NOT NULL,
 image text NOT NULL,
 payload jsonb NOT NULL,
 fetched_at timestamptz NOT NULL,
 PRIMARY KEY (account_id,image)
);
CREATE INDEX cluster_model_catalogs_fetched ON cluster_model_catalogs(fetched_at DESC);
