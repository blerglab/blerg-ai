-- Engine-scoped catalogs; retain the legacy table so older replicas and rollback
-- releases can continue using their original cache during a rolling deployment.
CREATE TABLE cluster_engine_model_catalogs (
 account_id text NOT NULL,
 engine text NOT NULL,
 image text NOT NULL,
 payload jsonb NOT NULL,
 fetched_at timestamptz NOT NULL,
 PRIMARY KEY (account_id,engine,image)
);
CREATE INDEX cluster_engine_model_catalogs_fetched ON cluster_engine_model_catalogs(fetched_at DESC);
INSERT INTO cluster_engine_model_catalogs(account_id,engine,image,payload,fetched_at)
 SELECT account_id,'codex',image,payload,fetched_at FROM cluster_model_catalogs;
