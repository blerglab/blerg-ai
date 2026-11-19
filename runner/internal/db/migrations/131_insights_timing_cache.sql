-- Timings of ended conversations are immutable until their evidence changes.
-- Cache only the derived classification, never commands, outputs or prose.
CREATE INDEX agent_events_insights_idx ON agent_events(session_id,kind,ts)
 WHERE kind IN ('mark','subagent_started','start_stage','status_changed','skill_load','skill_reporting','skill_evidence','skill_applicability');
CREATE TABLE session_insight_timings (
 session_id uuid PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
 timing jsonb NOT NULL,
 computed_at timestamptz NOT NULL DEFAULT now(),
 fingerprint text NOT NULL
);
-- Historical links warm on scoped reads in bounded batches, outside the
-- migration transaction and its trigger-definition locks.
CREATE TABLE session_insight_links_backfilled (
 session_id uuid PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE
);
CREATE FUNCTION invalidate_insight_timing() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 DELETE FROM session_insight_timings c USING sessions s WHERE c.session_id=NEW.session_id AND s.id=c.session_id AND s.ended_at IS NOT NULL;
 RETURN NEW;
END;
$$;
CREATE TRIGGER invalidate_insight_event AFTER INSERT ON agent_events FOR EACH ROW
 WHEN (NEW.kind IN ('tool_call','tool_result','status_changed','turn_done','mark','turn_work','background_task_started','background_task_done','process_activity','subagent_started'))
 EXECUTE FUNCTION invalidate_insight_timing();
CREATE TRIGGER invalidate_insight_wait AFTER INSERT OR UPDATE ON messages FOR EACH ROW
 WHEN (NEW.kind='ask') EXECUTE FUNCTION invalidate_insight_timing();

-- Title context includes the explicit launch board and current board grants.
-- A bare #N is never resolved across several boards.
CREATE FUNCTION card_link_context(sid uuid) RETURNS text LANGUAGE sql STABLE AS $$
 WITH RECURSIVE lineage AS (
  SELECT id,parent_session_id,spawning_account_id,0 AS depth FROM sessions WHERE id=sid
  UNION ALL SELECT p.id,p.parent_session_id,p.spawning_account_id,l.depth+1 FROM lineage l JOIN sessions p ON p.id=l.parent_session_id
  WHERE p.spawning_account_id=l.spawning_account_id AND l.depth<100
 ), contexts AS (
  SELECT s.board_id::text AS board,l.depth FROM lineage l JOIN sessions s ON s.id=l.id WHERE s.board_id IS NOT NULL
  UNION SELECT g.builtin_ref,l.depth FROM lineage l JOIN session_mcp_grants g ON g.session_id=l.id WHERE g.builtin='board' AND g.builtin_ref<>''
  UNION SELECT g.board_id,l.depth FROM lineage l JOIN session_proposal_grants g ON g.session_id=l.id WHERE g.kind='board_file' AND g.revoked_at IS NULL AND g.board_id<>''
  UNION SELECT c.board_id,l.depth FROM lineage l JOIN session_card_links c ON c.session_id=l.id WHERE c.source='pin'
 ) SELECT min(board) FROM contexts WHERE depth=(SELECT min(depth) FROM contexts) HAVING count(DISTINCT board)=1;
$$;
CREATE OR REPLACE FUNCTION link_card_title(sid uuid) RETURNS void LANGUAGE sql AS $$
 WITH context AS (SELECT card_link_context(sid) AS board)
 INSERT INTO session_card_links(session_id,board_id,card_number,source,linked_at)
 SELECT s.id,b.board,(m.n)[2]::integer,'title',s.started_at FROM sessions s CROSS JOIN context b
 CROSS JOIN LATERAL regexp_matches(coalesce(s.title,''),'(^|[[:space:]])#([1-9][0-9]{0,6})(?=$|[^[:alnum:]_-])','g') m(n)
 WHERE s.id=sid AND b.board IS NOT NULL ON CONFLICT DO NOTHING;
$$;
CREATE TRIGGER link_card_title_board AFTER UPDATE OF board_id ON sessions FOR EACH ROW EXECUTE FUNCTION link_card_title_session();
CREATE TRIGGER link_card_title_proposal_grant AFTER INSERT OR UPDATE ON session_proposal_grants FOR EACH ROW EXECUTE FUNCTION link_card_title_grant();

ALTER TABLE session_card_links DROP CONSTRAINT session_card_links_source_check;
ALTER TABLE session_card_links ADD CONSTRAINT session_card_links_source_check CHECK(source IN ('pin','title','review','failure','checklist'));
CREATE FUNCTION link_card_checklist(sid uuid, p jsonb, at_time timestamptz) RETURNS void LANGUAGE sql AS $$
 WITH context AS (SELECT card_link_context(sid) AS board), items AS (
  SELECT item FROM jsonb_array_elements(CASE WHEN jsonb_typeof(p->'items')='array' THEN p->'items' ELSE '[]'::jsonb END) item
  UNION ALL SELECT item FROM jsonb_array_elements(CASE WHEN jsonb_typeof(p->'updates')='array' THEN p->'updates' ELSE '[]'::jsonb END) item
  UNION ALL SELECT p->'changed' WHERE jsonb_typeof(p->'changed')='object'
 ) INSERT INTO session_card_links(session_id,board_id,card_number,source,linked_at)
 SELECT child.id,b.board,(m.n)[2]::integer,'checklist',at_time FROM items CROSS JOIN context b
 CROSS JOIN LATERAL regexp_matches(coalesce(item->>'text',''),'(^|[[:space:]])#([1-9][0-9]{0,6})(?=$|[^[:alnum:]_-])','g') m(n)
 CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(item->'assignees')='array' THEN item->'assignees' ELSE '[]'::jsonb END) worker
 JOIN sessions child ON child.id::text=worker->>'id' AND child.parent_session_id=sid
 JOIN sessions parent ON parent.id=sid AND child.spawning_account_id=parent.spawning_account_id
 WHERE worker->>'kind'='child_session' AND b.board IS NOT NULL ON CONFLICT DO NOTHING;
$$;
CREATE FUNCTION link_card_checklist_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM link_card_checklist(NEW.session_id,NEW.payload,NEW.ts);
 RETURN NEW;
END;
$$;
CREATE TRIGGER link_card_checklist_event AFTER INSERT ON agent_events FOR EACH ROW WHEN (NEW.kind='checklist') EXECUTE FUNCTION link_card_checklist_event();

-- New parent context can resolve titles which existed before the pin. Touch
-- only that owner's descendants; preserve the session's original ranking date.
CREATE FUNCTION link_card_descendant_titles() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.source='pin' THEN
  PERFORM link_card_title(d.id) FROM (
   WITH RECURSIVE descendants AS (
    SELECT s.id,s.spawning_account_id FROM sessions s WHERE s.id=NEW.session_id
    UNION SELECT s.id,s.spawning_account_id FROM sessions s JOIN descendants p ON s.parent_session_id=p.id AND s.spawning_account_id=p.spawning_account_id
   ) SELECT id FROM descendants
  ) d;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER link_card_descendant_titles AFTER INSERT ON session_card_links FOR EACH ROW WHEN (NEW.source='pin') EXECUTE FUNCTION link_card_descendant_titles();
