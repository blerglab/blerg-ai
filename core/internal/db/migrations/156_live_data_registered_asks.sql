-- Legacy proposals were not tied to a registered Runner question. Never approve them.
UPDATE live_data_proposals SET status='declined',ask_id=gen_random_uuid() WHERE status='pending' AND ask_id IS NULL;
CREATE OR REPLACE FUNCTION live_data_ask_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.ask_id IS DISTINCT FROM OLD.ask_id THEN
  RAISE EXCEPTION 'immutable_live_data_ask' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS live_data_ask_immutable ON live_data_proposals;
CREATE TRIGGER live_data_ask_immutable BEFORE UPDATE ON live_data_proposals FOR EACH ROW EXECUTE FUNCTION live_data_ask_immutable();
