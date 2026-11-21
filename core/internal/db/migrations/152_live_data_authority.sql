-- Inert until the human approval endpoint and service transport are reviewed.
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS live_data_epoch bigint NOT NULL DEFAULT 1 CHECK (live_data_epoch>0);
ALTER TABLE mcp_connections ADD COLUMN IF NOT EXISTS live_data_generation bigint NOT NULL DEFAULT 1 CHECK (live_data_generation>0);
CREATE TABLE IF NOT EXISTS live_data_policy (
 connection_id uuid NOT NULL REFERENCES mcp_connections(id) ON DELETE CASCADE,
 tool text NOT NULL,
 mode text NOT NULL DEFAULT 'off' CHECK (mode IN ('off','propose','allow')),
 hash text NOT NULL CHECK (hash ~ '^[a-f0-9]{64}$'),
 PRIMARY KEY(connection_id,tool)
);
CREATE TABLE IF NOT EXISTS live_data_grants (
 binding_id uuid NOT NULL,
 revision bigint NOT NULL CHECK (revision>0),
 owner_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 connection_id uuid NOT NULL REFERENCES mcp_connections(id) ON DELETE CASCADE,
 scope jsonb NOT NULL,
 arguments jsonb NOT NULL CHECK (jsonb_typeof(arguments)='object' AND octet_length(arguments::text)<=65536),
 approved_sid text NOT NULL CHECK (approved_sid<>''),
 approved_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL CHECK (expires_at>approved_at AND expires_at<=approved_at+interval '30 days'),
 revoked_at timestamptz,
 unsafe_host_opt_in boolean NOT NULL DEFAULT false,
 PRIMARY KEY(binding_id,revision)
);
-- No FK: audit metadata survives disabled accounts and removed grants.
CREATE TABLE IF NOT EXISTS live_data_credential_audit (
 id bigserial PRIMARY KEY,
 binding_id uuid,
 revision bigint,
 owner_id uuid,
 installation_id uuid,
 runner_id uuid,
 key_id text,
 authority_epoch bigint,
 operation text NOT NULL CHECK (operation IN ('mint','renew','check','revoke')),
 outcome text NOT NULL CHECK (outcome IN ('ok','denied','unavailable','unsafe_host','clock_skew')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX IF NOT EXISTS live_data_credential_audit_retention ON live_data_credential_audit(created_at);
CREATE OR REPLACE FUNCTION live_data_connection_fence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status IS DISTINCT FROM OLD.status OR NEW.url IS DISTINCT FROM OLD.url OR NEW.auth_kind IS DISTINCT FROM OLD.auth_kind OR NEW.header_name IS DISTINCT FROM OLD.header_name OR (NEW.auth_kind<>'oauth' AND NEW.secret_ciphertext IS DISTINCT FROM OLD.secret_ciphertext) THEN
  NEW.live_data_generation := OLD.live_data_generation+1;
  UPDATE live_data_grants SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE connection_id=OLD.id;
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS live_data_connection_fence ON mcp_connections;
CREATE TRIGGER live_data_connection_fence BEFORE UPDATE ON mcp_connections FOR EACH ROW EXECUTE FUNCTION live_data_connection_fence();
CREATE OR REPLACE FUNCTION live_data_policy_fence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  UPDATE live_data_grants SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE connection_id=OLD.connection_id AND scope->>'tool'=OLD.tool;
  RETURN OLD;
 END IF;
 IF NEW.mode<>'allow' OR NEW.hash IS DISTINCT FROM OLD.hash THEN
  UPDATE live_data_grants SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE connection_id=OLD.connection_id AND scope->>'tool'=OLD.tool;
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS live_data_policy_fence ON live_data_policy;
CREATE TRIGGER live_data_policy_fence AFTER UPDATE OR DELETE ON live_data_policy FOR EACH ROW EXECUTE FUNCTION live_data_policy_fence();
CREATE OR REPLACE FUNCTION live_data_owner_fence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.disabled_at IS NOT NULL AND OLD.disabled_at IS NULL THEN
  NEW.live_data_epoch := OLD.live_data_epoch+1;
 END IF;
 IF NEW.live_data_epoch<>OLD.live_data_epoch THEN
  UPDATE live_data_grants SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE owner_id=OLD.id;
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS live_data_owner_fence ON accounts;
CREATE TRIGGER live_data_owner_fence BEFORE UPDATE ON accounts FOR EACH ROW EXECUTE FUNCTION live_data_owner_fence();
-- Material approval data is append-only. Revocation and lowering host opt-in
-- may change in place; an expanded grant always requires a new approved revision.
CREATE OR REPLACE FUNCTION live_data_grant_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.binding_id<>OLD.binding_id OR NEW.revision<>OLD.revision OR NEW.owner_id<>OLD.owner_id OR NEW.connection_id<>OLD.connection_id OR NEW.scope<>OLD.scope OR NEW.arguments<>OLD.arguments OR NEW.approved_sid<>OLD.approved_sid OR NEW.approved_at<>OLD.approved_at OR NEW.expires_at<>OLD.expires_at OR (NEW.unsafe_host_opt_in AND NOT OLD.unsafe_host_opt_in) OR (OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS DISTINCT FROM OLD.revoked_at) THEN
  RAISE EXCEPTION 'immutable_live_data_revision' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS live_data_grant_immutable ON live_data_grants;
CREATE TRIGGER live_data_grant_immutable BEFORE UPDATE ON live_data_grants FOR EACH ROW EXECUTE FUNCTION live_data_grant_immutable();
