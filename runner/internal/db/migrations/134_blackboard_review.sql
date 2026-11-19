ALTER TABLE plan_events DROP CONSTRAINT plan_events_pkey;
ALTER TABLE plan_events ADD PRIMARY KEY(blackboard_id,id);
-- Earlier global-ID conflicts left the event in its owning JSON record but
-- silently omitted its relational row. Recover those rows without rewriting
-- any existing append-only event.
INSERT INTO plan_events(id,blackboard_id,milestone_id,authenticated_actor,recorded_at,claimed_at,source,kind,payload,data)
 SELECT e->>'id',b.id,e->>'milestone_id',e->'authenticated_actor',
 (e->>'recorded_at')::timestamptz,(e->>'claimed_at')::timestamptz,
 e->>'source',e->>'kind',COALESCE(NULLIF(e->'payload','null'::jsonb),'{}'::jsonb),e
 FROM blackboards b CROSS JOIN LATERAL jsonb_array_elements(
 CASE WHEN jsonb_typeof(b.record->'events')='array' THEN b.record->'events' ELSE '[]'::jsonb END) e WHERE true
 ON CONFLICT(blackboard_id,id) DO NOTHING;
CREATE INDEX blackboards_workspace_idx ON blackboards((record->>'workspace_id'),id);
CREATE INDEX blackboard_links_workspace_idx ON blackboard_links(workspace_id,blackboard_id);

-- An incomplete sample can be repaired once by a real, later observation.
-- Complete samples, identity and deletion remain immutable.
CREATE FUNCTION blackboard_snapshot_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'UPDATE' THEN
  IF NOT OLD.complete AND NEW.complete
   AND NEW.blackboard_id=OLD.blackboard_id AND NEW.milestone_id=OLD.milestone_id
   AND NEW.day=OLD.day AND NEW.observed_at>=OLD.observed_at THEN RETURN NEW; END IF;
 END IF;
 RAISE EXCEPTION 'blackboard snapshot is immutable';
END $$;
DROP TRIGGER milestone_snapshots_immutable ON milestone_snapshots;
CREATE TRIGGER milestone_snapshots_immutable BEFORE UPDATE OR DELETE ON milestone_snapshots FOR EACH ROW EXECUTE FUNCTION blackboard_snapshot_guard();
