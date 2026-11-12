ALTER TABLE live_data_grants ADD COLUMN IF NOT EXISTS approved_configuration bytea;
-- Old incomplete grants cannot be upgraded into a new approval.
UPDATE live_data_grants SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE approved_configuration IS NULL;
DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='live_data_grants'::regclass AND conname='live_data_complete_grant') THEN
  ALTER TABLE live_data_grants ADD CONSTRAINT live_data_complete_grant CHECK(revoked_at IS NOT NULL OR approved_configuration IS NOT NULL);
 END IF;
END $$;
CREATE OR REPLACE FUNCTION live_data_configuration_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.approved_configuration IS DISTINCT FROM OLD.approved_configuration THEN
  RAISE EXCEPTION 'immutable_live_data_configuration' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS live_data_configuration_immutable ON live_data_grants;
CREATE TRIGGER live_data_configuration_immutable BEFORE UPDATE ON live_data_grants FOR EACH ROW EXECUTE FUNCTION live_data_configuration_immutable();
