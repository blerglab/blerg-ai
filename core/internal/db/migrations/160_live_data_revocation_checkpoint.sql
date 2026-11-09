-- A revocation cannot commit until Core has fsynced/CAS-written its reservation
-- outside this database. Sequences expose reservations before transaction commit.
-- They are coordination, not an authorization boundary against a database admin.
CREATE SEQUENCE IF NOT EXISTS live_data_revocation_requested;
CREATE SEQUENCE IF NOT EXISTS live_data_revocation_acknowledged;
CREATE TABLE IF NOT EXISTS live_data_revocation_checkpoint (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 authority_epoch bigint NOT NULL DEFAULT 0 CHECK(authority_epoch>=0),
 high_water bigint NOT NULL DEFAULT 0 CHECK(high_water>=0),
 guarded boolean NOT NULL DEFAULT false
);
INSERT INTO live_data_revocation_checkpoint(singleton) VALUES(true) ON CONFLICT DO NOTHING;
CREATE OR REPLACE FUNCTION live_data_checkpoint_revocation() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE ticket bigint; enforce boolean; deadline timestamptz;
BEGIN
 IF OLD.revoked_at IS NOT NULL OR (TG_OP='UPDATE' AND NEW.revoked_at IS NULL) THEN
  IF TG_OP='DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
 END IF;
 SELECT guarded INTO enforce FROM live_data_revocation_checkpoint WHERE singleton FOR UPDATE;
 ticket := nextval('live_data_revocation_requested');
 IF enforce THEN
  deadline := clock_timestamp()+interval '5 seconds';
  LOOP
   EXIT WHEN (SELECT CASE WHEN is_called THEN last_value ELSE 0 END FROM live_data_revocation_acknowledged)>=ticket;
   IF clock_timestamp()>=deadline THEN
    RAISE EXCEPTION 'live_data_revocation_checkpoint_unavailable' USING ERRCODE='57014';
   END IF;
   PERFORM pg_sleep(0.01);
  END LOOP;
 END IF;
 UPDATE live_data_revocation_checkpoint SET high_water=ticket WHERE singleton;
 IF TG_OP='DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
END $$;
DROP TRIGGER IF EXISTS live_data_checkpoint_revocation ON live_data_grants;
CREATE TRIGGER live_data_checkpoint_revocation BEFORE UPDATE OF revoked_at OR DELETE ON live_data_grants FOR EACH ROW EXECUTE FUNCTION live_data_checkpoint_revocation();
