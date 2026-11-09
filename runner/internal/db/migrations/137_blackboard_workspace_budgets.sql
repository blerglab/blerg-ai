-- Worker and blackboard identities cannot reset aggregate workspace admission.
CREATE TABLE blackboard_workspace_budgets (
 workspace_id text NOT NULL, scope_id text NOT NULL,
 event_count bigint NOT NULL DEFAULT 0, event_bytes bigint NOT NULL DEFAULT 0,
 PRIMARY KEY(workspace_id,scope_id)
);
INSERT INTO blackboard_workspace_budgets
 SELECT COALESCE(NULLIF(b.record->>'workspace_id',''),b.id),COALESCE(e.milestone_id,''),count(*),sum(octet_length(e.data::text))
 FROM plan_events e JOIN blackboards b ON b.id=e.blackboard_id WHERE e.source='agent'
 GROUP BY COALESCE(NULLIF(b.record->>'workspace_id',''),b.id),COALESCE(e.milestone_id,'');
