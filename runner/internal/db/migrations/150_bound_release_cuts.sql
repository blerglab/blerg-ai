-- A cut answer authorizes an outbox entry. Immutable causal links, not clocks,
-- order runner cuts; only native board cuts use the conservative clock margin.
CREATE TABLE blackboard_release_links (
 id bigserial PRIMARY KEY,
 blackboard_id text NOT NULL,
 milestone_id text NOT NULL,
 board_id text NOT NULL,
 release_id text NOT NULL,
 revision integer NOT NULL,
 origin_ask_id uuid NOT NULL REFERENCES bound_asks(message_id),
 linked_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX blackboard_release_links_latest ON blackboard_release_links(milestone_id,id DESC);
CREATE FUNCTION immutable_cut_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'cut evidence is immutable'; END $$;
CREATE TRIGGER blackboard_release_links_immutable BEFORE UPDATE OR DELETE ON blackboard_release_links FOR EACH ROW EXECUTE FUNCTION immutable_cut_evidence();
CREATE TABLE blackboard_cut_executions (
 ask_id uuid PRIMARY KEY REFERENCES bound_asks(message_id),
 workspace_id uuid NOT NULL REFERENCES sessions(id),
 blackboard_id text NOT NULL,
 milestone_id text NOT NULL,
 link_id bigint NOT NULL REFERENCES blackboard_release_links(id),
 principal jsonb NOT NULL,
 authorized_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 deadline timestamptz NOT NULL DEFAULT clock_timestamp()+interval '24 hours',
 status text NOT NULL DEFAULT 'authorized' CHECK(status IN ('authorized','executing','executed','failed')),
 cause text NOT NULL DEFAULT '',
 next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 lease_until timestamptz,
 failure_message_id uuid REFERENCES messages(id),
 receipt jsonb
);
CREATE FUNCTION cut_authorization_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.ask_id,NEW.workspace_id,NEW.blackboard_id,NEW.milestone_id,NEW.link_id,NEW.principal,NEW.authorized_at,NEW.deadline)
 IS DISTINCT FROM ROW(OLD.ask_id,OLD.workspace_id,OLD.blackboard_id,OLD.milestone_id,OLD.link_id,OLD.principal,OLD.authorized_at,OLD.deadline) THEN RAISE EXCEPTION 'cut authorization is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER cut_authorization_immutable BEFORE UPDATE ON blackboard_cut_executions FOR EACH ROW EXECUTE FUNCTION cut_authorization_immutable();
CREATE TRIGGER cut_authorization_no_delete BEFORE DELETE ON blackboard_cut_executions FOR EACH ROW EXECUTE FUNCTION immutable_cut_evidence();
CREATE TABLE blackboard_cut_finalizations (
 milestone_id text PRIMARY KEY,
 blackboard_id text NOT NULL,
 receipt_id uuid NOT NULL UNIQUE,
 ask_id uuid,
 frozen_at timestamptz NOT NULL,
 receipt jsonb NOT NULL,
 frozen jsonb NOT NULL
);
CREATE TRIGGER blackboard_cut_finalizations_immutable BEFORE UPDATE OR DELETE ON blackboard_cut_finalizations FOR EACH ROW EXECUTE FUNCTION immutable_cut_evidence();
CREATE TABLE blackboard_cut_observations (
 milestone_id text PRIMARY KEY,
 observed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 next_check_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 known_released boolean NOT NULL DEFAULT false,
 cause text NOT NULL
);
CREATE TABLE blackboard_cut_legacy_evidence (
 id bigserial PRIMARY KEY,
 blackboard_id text NOT NULL,
 milestone_id text NOT NULL,
 archived_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 snapshot jsonb NOT NULL
);
CREATE INDEX blackboard_cut_legacy_evidence_milestone ON blackboard_cut_legacy_evidence(milestone_id,id DESC);
CREATE TRIGGER blackboard_cut_legacy_evidence_immutable BEFORE UPDATE OR DELETE ON blackboard_cut_legacy_evidence FOR EACH ROW EXECUTE FUNCTION immutable_cut_evidence();
-- Clear old transient locks with a canonical audit event; preserve old evidence.
WITH legacy AS (
 SELECT m.*,gen_random_uuid()::text AS event_id,clock_timestamp() AS at FROM milestones m WHERE stage IN ('Final','Finalizing')
), preserved AS (
 INSERT INTO blackboard_cut_legacy_evidence(blackboard_id,milestone_id,snapshot) SELECT blackboard_id,id,data FROM legacy RETURNING id,milestone_id
), audit AS (
 SELECT l.*,p.id AS legacy_evidence_id,jsonb_build_object('id',event_id,'milestone_id',l.id,'authenticated_actor',jsonb_build_object('kind','system','id','runner'),
 'recorded_at',at,'source','derived','kind','legacy_cut_evidence_incomplete','payload',jsonb_build_object('previous_stage',stage,'legacy_frozen_at',frozen_at,'legacy_evidence_id',p.id)) AS event FROM legacy l JOIN preserved p ON p.milestone_id=l.id
), inserted AS (
 INSERT INTO plan_events(id,blackboard_id,milestone_id,authenticated_actor,recorded_at,source,kind,payload,data)
 SELECT event_id,blackboard_id,id,event->'authenticated_actor',at,'derived','legacy_cut_evidence_incomplete',event->'payload',event FROM audit RETURNING milestone_id
)
UPDATE milestones m SET version=m.version+1,stage='Evidence incomplete',frozen_at=CASE WHEN m.stage='Finalizing' THEN NULL ELSE m.frozen_at END,
 data=m.data||jsonb_build_object('stage','Evidence incomplete','version',m.version+1,'evidence_incomplete',true,'legacy_evidence_id',(SELECT legacy_evidence_id FROM audit WHERE audit.id=m.id),'legacy_frozen_at',m.frozen_at,'frozen_at',CASE WHEN m.stage='Finalizing' THEN NULL ELSE m.frozen_at END)
WHERE m.id IN (SELECT milestone_id FROM inserted);
-- Core reads use the lean header, while frozen bodies remain in milestones.
-- Keep both projections coherent before serving any post-migration read.
UPDATE blackboards b SET version=b.version+1,
 record=jsonb_set(jsonb_set(b.record,'{milestones}',(
 SELECT jsonb_agg(CASE WHEN m.id IS NOT NULL THEN m.data-'frozen' ELSE entry.value END ORDER BY entry.ordinality)
 FROM jsonb_array_elements(b.record->'milestones') WITH ORDINALITY AS entry(value,ordinality)
 LEFT JOIN milestones m ON m.blackboard_id=b.id AND m.id=entry.value->>'id'
 )),'{version}',to_jsonb(b.version+1))
WHERE EXISTS(SELECT 1 FROM blackboard_cut_legacy_evidence e WHERE e.blackboard_id=b.id)
 AND jsonb_typeof(b.record->'milestones')='array' AND jsonb_array_length(b.record->'milestones')>0;
