-- Match the access-layer fallback for records linked before workspace_id existed.
-- Rebuild counters from immutable event rows, preserving all audit history.
TRUNCATE blackboard_workspace_budgets;
INSERT INTO blackboard_workspace_budgets
 SELECT COALESCE(NULLIF(b.record->>'workspace_id',''),NULLIF(b.record->'links'->0->>'workspace_id',''),b.id),COALESCE(e.milestone_id,''),count(*),sum(octet_length(e.data::text))
 FROM plan_events e JOIN blackboards b ON b.id=e.blackboard_id WHERE e.source='agent'
 GROUP BY COALESCE(NULLIF(b.record->>'workspace_id',''),NULLIF(b.record->'links'->0->>'workspace_id',''),b.id),COALESCE(e.milestone_id,'');
