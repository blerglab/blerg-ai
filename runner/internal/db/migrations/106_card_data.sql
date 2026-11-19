-- Structured reviews remain canonical agent events, with an index for per-card reads.
CREATE INDEX agent_events_review_card_idx ON agent_events ((payload->>'board_id'),(payload->>'card'),session_id)
 WHERE kind='mark' AND payload->>'kind'='review';

-- Keep links after unpin or title edits. Board and number together identify a card.
CREATE TABLE session_card_links (
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 board_id text NOT NULL,
 card_number integer NOT NULL CHECK(card_number>0 AND card_number<=10000000),
 source text NOT NULL CHECK(source IN ('pin','title','review')),
 linked_at timestamptz NOT NULL,
 PRIMARY KEY(session_id,board_id,card_number,source)
);
CREATE INDEX session_card_links_card_idx ON session_card_links(board_id,card_number);

INSERT INTO session_card_links
 SELECT DISTINCT ON(session_id,payload->'ticket'->>'board_id',payload->'ticket'->>'key')
 session_id,payload->'ticket'->>'board_id',substring(payload->'ticket'->>'key' from '^#([0-9]{1,7})$')::integer,'pin',ts
 FROM agent_events WHERE kind='ticket' AND payload->>'action' IN ('pin','update') AND payload->'ticket'->>'source'='blerg-board'
 AND coalesce(payload->'ticket'->>'board_id','')<>'' AND payload->'ticket'->>'key' ~ '^#[1-9][0-9]{0,6}$'
 ORDER BY session_id,payload->'ticket'->>'board_id',payload->'ticket'->>'key',ts;

-- A bare #N needs an unambiguous board context. Use the launch board, never
-- every board the owner can access. Keep title links when a title later changes.
CREATE FUNCTION link_card_title(sid uuid) RETURNS void LANGUAGE sql AS $$
 WITH contexts AS (
  SELECT builtin_ref AS board FROM session_mcp_grants WHERE session_id=sid AND builtin='board' AND builtin_ref<>''
  UNION SELECT board_id FROM session_card_links WHERE session_id=sid AND source='pin'
 ), context AS (SELECT min(board) AS board FROM contexts HAVING count(*)=1)
 INSERT INTO session_card_links(session_id,board_id,card_number,source,linked_at)
 SELECT s.id,b.board,(m.n)[2]::integer,'title',now() FROM sessions s CROSS JOIN context b
 CROSS JOIN LATERAL regexp_matches(coalesce(s.title,''),'(^|[[:space:]])#([1-9][0-9]{0,6})(?=$|[^[:alnum:]_-])','g') m(n)
 WHERE s.id=sid ON CONFLICT DO NOTHING;
$$;
CREATE FUNCTION link_card_title_session() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN PERFORM link_card_title(NEW.id); RETURN NEW; END;
$$;
CREATE TRIGGER link_card_title_session AFTER INSERT OR UPDATE OF title ON sessions FOR EACH ROW EXECUTE FUNCTION link_card_title_session();
CREATE FUNCTION link_card_title_grant() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN PERFORM link_card_title(NEW.session_id); RETURN NEW; END;
$$;
CREATE TRIGGER link_card_title_grant AFTER INSERT OR UPDATE OF builtin_ref ON session_mcp_grants FOR EACH ROW EXECUTE FUNCTION link_card_title_grant();
SELECT link_card_title(id) FROM sessions;

-- Pinning gives otherwise unscoped workspace titles their board context.
CREATE FUNCTION link_card_event() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b text; n text; src text;
BEGIN
 IF NEW.kind='ticket' AND NEW.payload->>'action' IN ('pin','update') AND NEW.payload->'ticket'->>'source'='blerg-board' THEN
  b:=NEW.payload->'ticket'->>'board_id'; n:=substring(NEW.payload->'ticket'->>'key' from '^#([0-9]{1,7})$'); src:='pin';
 ELSIF NEW.kind='mark' AND NEW.payload->>'kind'='review' THEN
  b:=NEW.payload->>'board_id'; n:=NEW.payload->>'card'; src:='review';
 END IF;
 IF coalesce(b,'')<>'' AND coalesce(n,'') ~ '^[0-9]{1,7}$' AND n::integer>0 THEN
  INSERT INTO session_card_links VALUES(NEW.session_id,b,n::integer,src,NEW.ts) ON CONFLICT DO NOTHING;
  PERFORM link_card_title(NEW.session_id);
 END IF;
 RETURN NEW;
END;
$$;

CREATE TRIGGER link_card_event AFTER INSERT ON agent_events FOR EACH ROW EXECUTE FUNCTION link_card_event();
