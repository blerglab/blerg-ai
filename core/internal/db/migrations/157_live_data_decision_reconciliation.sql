ALTER TABLE live_data_grants ADD COLUMN IF NOT EXISTS runner_committed boolean NOT NULL DEFAULT false;
CREATE OR REPLACE FUNCTION live_data_proposal_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.binding_id,NEW.revision,NEW.owner_id,NEW.digest,NEW.configuration,NEW.created_at,NEW.decision_deadline) IS DISTINCT FROM ROW(OLD.binding_id,OLD.revision,OLD.owner_id,OLD.digest,OLD.configuration,OLD.created_at,OLD.decision_deadline)
 OR (OLD.status<>'pending' AND NOT (OLD.status='approved' AND NEW.status='declined') AND ROW(NEW.status,NEW.ask_id,NEW.approved_sid) IS DISTINCT FROM ROW(OLD.status,OLD.ask_id,OLD.approved_sid))
 OR (OLD.approved_sid IS NOT NULL AND NEW.approved_sid IS DISTINCT FROM OLD.approved_sid) THEN
  RAISE EXCEPTION 'immutable_live_binding_proposal' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
