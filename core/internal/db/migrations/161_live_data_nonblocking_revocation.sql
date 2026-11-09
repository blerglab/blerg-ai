-- Human security changes commit a local deny and durable retry ticket without
-- waiting for the optional external authority store. Polling checks refuse while
-- committed tickets remain unacknowledged. Core startup invalidates existing
-- approvals, including uncheckpointed restored snapshots. Checkpointed rollback
-- is additionally refused against the independent authority high-water mark.
CREATE OR REPLACE FUNCTION live_data_checkpoint_revocation() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE ticket bigint;
BEGIN
 IF OLD.revoked_at IS NOT NULL OR (TG_OP='UPDATE' AND NEW.revoked_at IS NULL) THEN
  IF TG_OP='DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
 END IF;
 PERFORM 1 FROM live_data_revocation_checkpoint WHERE singleton FOR UPDATE;
 ticket := nextval('live_data_revocation_requested');
 UPDATE live_data_revocation_checkpoint SET high_water=ticket WHERE singleton;
 IF TG_OP='DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
END $$;
