ALTER TABLE live_data_credential_audit ADD COLUMN IF NOT EXISTS attempt_id uuid;
CREATE INDEX IF NOT EXISTS live_data_credential_attempt ON live_data_credential_audit(attempt_id) WHERE attempt_id IS NOT NULL;
ALTER TABLE mcp_secret_access_log ADD COLUMN IF NOT EXISTS fetched_by_live_attempt_id uuid;
