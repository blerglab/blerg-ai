-- Failures share the canonical marks contract; session alerts keep their own source.
ALTER TABLE session_card_links DROP CONSTRAINT session_card_links_source_check;
ALTER TABLE session_card_links ADD CONSTRAINT session_card_links_source_check CHECK(source IN ('pin','title','review','failure'));
CREATE INDEX agent_events_failure_card_idx ON agent_events ((payload->>'board_id'),(payload->>'card'),session_id) WHERE kind='mark' AND payload->>'kind'='failure';

CREATE OR REPLACE FUNCTION link_card_event() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b text; n text; src text;
BEGIN
 IF NEW.kind='ticket' AND NEW.payload->>'action' IN ('pin','update') AND NEW.payload->'ticket'->>'source'='blerg-board' THEN
  b:=NEW.payload->'ticket'->>'board_id'; n:=substring(NEW.payload->'ticket'->>'key' from '^#([0-9]{1,7})$'); src:='pin';
 ELSIF NEW.kind='mark' AND NEW.payload->>'kind' IN ('review','failure') THEN
  b:=NEW.payload->>'board_id'; n:=NEW.payload->>'card'; src:=NEW.payload->>'kind';
 END IF;
 IF coalesce(b,'')<>'' AND coalesce(n,'') ~ '^[0-9]{1,7}$' AND n::integer>0 THEN
  INSERT INTO session_card_links VALUES(NEW.session_id,b,n::integer,src,NEW.ts) ON CONFLICT DO NOTHING;
  PERFORM link_card_title(NEW.session_id);
 END IF;
 RETURN NEW;
END;
$$;

