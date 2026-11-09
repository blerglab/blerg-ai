-- The JSON header contains only the person's plan. Audit and proposal rows are
-- authoritative; historical payload volume is never a lifecycle write budget.
CREATE SEQUENCE blackboard_row_sequence;
ALTER TABLE plan_events ADD COLUMN ordinal bigint NOT NULL DEFAULT nextval('blackboard_row_sequence');
ALTER TABLE plan_proposals ADD COLUMN ordinal bigint NOT NULL DEFAULT nextval('blackboard_row_sequence');
-- Preserve the exact old append order, including equal observation timestamps.
DROP TRIGGER plan_events_immutable ON plan_events;
UPDATE plan_events p SET ordinal=e.n FROM blackboards b,
 LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(b.record->'events')='array' THEN b.record->'events' ELSE '[]'::jsonb END)
 WITH ORDINALITY e(data,n) WHERE p.blackboard_id=b.id AND p.id=e.data->>'id';
CREATE TRIGGER plan_events_immutable BEFORE UPDATE OR DELETE ON plan_events FOR EACH ROW EXECUTE FUNCTION blackboard_history_immutable();
UPDATE plan_proposals p SET ordinal=e.n FROM blackboards b,
 LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(b.record->'proposals')='array' THEN b.record->'proposals' ELSE '[]'::jsonb END)
 WITH ORDINALITY e(data,n) WHERE p.blackboard_id=b.id AND p.id=e.data->>'id';
SELECT setval('blackboard_row_sequence',GREATEST(1,COALESCE((SELECT max(ordinal) FROM plan_events),0),COALESCE((SELECT max(ordinal) FROM plan_proposals),0)));
CREATE INDEX plan_events_partition_idx ON plan_events(blackboard_id,(COALESCE(milestone_id,'')),source,kind,ordinal DESC);
CREATE INDEX plan_proposals_pending_idx ON plan_proposals(blackboard_id,(COALESCE(milestone_id,'')),ordinal) WHERE status='pending';
CREATE INDEX milestones_blackboard_idx ON milestones(blackboard_id,id);
CREATE TABLE blackboard_partition_budgets (
 blackboard_id text NOT NULL REFERENCES blackboards(id), scope_id text NOT NULL,
 kind text NOT NULL, event_count integer NOT NULL DEFAULT 0,
 event_bytes bigint NOT NULL DEFAULT 0, PRIMARY KEY(blackboard_id,scope_id,kind)
);
INSERT INTO blackboard_partition_budgets
 SELECT blackboard_id,COALESCE(milestone_id,''),CASE source WHEN 'agent' THEN 'event' ELSE 'observation' END,
 count(*),sum(octet_length(data::text)) FROM plan_events WHERE source IN ('agent','board')
 GROUP BY blackboard_id,COALESCE(milestone_id,''),source;
CREATE TABLE blackboard_observations (
 blackboard_id text NOT NULL REFERENCES blackboards(id), scope_id text NOT NULL,
 board_id text NOT NULL, card_number integer NOT NULL, data jsonb NOT NULL,
 PRIMARY KEY(blackboard_id,scope_id,board_id,card_number)
);
INSERT INTO blackboard_observations
 SELECT DISTINCT ON (blackboard_id,COALESCE(milestone_id,''),payload->>'board_id',payload->>'card_number')
 blackboard_id,COALESCE(milestone_id,''),payload->>'board_id',(payload->>'card_number')::integer,data
 FROM plan_events WHERE source='board' AND COALESCE(payload->>'board_id','')<>''
 AND payload->>'card_number' ~ '^[1-9][0-9]*$'
 ORDER BY blackboard_id,COALESCE(milestone_id,''),payload->>'board_id',payload->>'card_number',ordinal DESC;
UPDATE blackboards SET record=record-ARRAY['proposals','events','revisions','snapshots'];
-- Cut observations are already mirrored in milestones.data. They are loaded
-- only for cut retries and frozen reads, never for commit, reject or abort.
UPDATE blackboards b SET record=jsonb_set(record,'{milestones}',
 (SELECT COALESCE(jsonb_agg(m.data-'frozen' ORDER BY m.n),'[]'::jsonb)
 FROM jsonb_array_elements(b.record->'milestones') WITH ORDINALITY m(data,n)))
 WHERE jsonb_typeof(record->'milestones')='array';
