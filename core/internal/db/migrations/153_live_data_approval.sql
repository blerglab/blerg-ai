CREATE TABLE IF NOT EXISTS live_data_proposal_heads (
 binding_id uuid PRIMARY KEY,
 owner_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 revision bigint NOT NULL CHECK(revision>=0)
);
CREATE TABLE IF NOT EXISTS live_data_proposals (
 binding_id uuid NOT NULL REFERENCES live_data_proposal_heads(binding_id) ON DELETE CASCADE,
 revision bigint NOT NULL CHECK(revision>0),
 owner_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 digest text NOT NULL CHECK(digest ~ '^[a-f0-9]{64}$'),
 configuration bytea NOT NULL CHECK(octet_length(configuration)<=65536),
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','approved','declined')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 decision_deadline timestamptz NOT NULL DEFAULT clock_timestamp()+interval '24 hours',
 ask_id uuid,
 approved_sid text,
 PRIMARY KEY(binding_id,revision),
 UNIQUE(ask_id)
);
CREATE TABLE IF NOT EXISTS live_data_owner_settings (
 account_id uuid PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
 unsafe_host_opt_in boolean NOT NULL DEFAULT false
);
CREATE TABLE IF NOT EXISTS live_data_approval_audit (
 id bigserial PRIMARY KEY,
 owner_id uuid,
 binding_id uuid,
 revision bigint,
 digest text,
 ask_id uuid,
 operation text NOT NULL CHECK(operation IN ('approve','decline','policy','host_opt_in')),
 outcome text NOT NULL CHECK(outcome IN ('ok','denied')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX IF NOT EXISTS live_data_approval_audit_retention ON live_data_approval_audit(created_at);
CREATE OR REPLACE FUNCTION live_data_proposal_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.binding_id,NEW.revision,NEW.owner_id,NEW.digest,NEW.configuration,NEW.created_at,NEW.decision_deadline) IS DISTINCT FROM ROW(OLD.binding_id,OLD.revision,OLD.owner_id,OLD.digest,OLD.configuration,OLD.created_at,OLD.decision_deadline) OR (OLD.status<>'pending' AND ROW(NEW.status,NEW.ask_id,NEW.approved_sid) IS DISTINCT FROM ROW(OLD.status,OLD.ask_id,OLD.approved_sid)) THEN
  RAISE EXCEPTION 'immutable_live_binding_proposal' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS live_data_proposal_immutable ON live_data_proposals;
CREATE TRIGGER live_data_proposal_immutable BEFORE UPDATE ON live_data_proposals FOR EACH ROW EXECUTE FUNCTION live_data_proposal_immutable();
CREATE OR REPLACE FUNCTION live_data_host_opt_out() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.unsafe_host_opt_in AND NOT NEW.unsafe_host_opt_in THEN
  UPDATE live_data_grants SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE owner_id=NEW.account_id AND unsafe_host_opt_in;
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS live_data_host_opt_out ON live_data_owner_settings;
CREATE TRIGGER live_data_host_opt_out AFTER UPDATE ON live_data_owner_settings FOR EACH ROW EXECUTE FUNCTION live_data_host_opt_out();
