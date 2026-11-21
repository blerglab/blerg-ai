-- Agent admission belongs to the authenticated actor, never its neighbours.
ALTER TABLE blackboard_partition_budgets ADD COLUMN actor_id text NOT NULL DEFAULT '';
ALTER TABLE blackboard_partition_budgets DROP CONSTRAINT blackboard_partition_budgets_pkey;
ALTER TABLE blackboard_partition_budgets ADD PRIMARY KEY(blackboard_id,scope_id,kind,actor_id);
DELETE FROM blackboard_partition_budgets WHERE kind='event';
INSERT INTO blackboard_partition_budgets(blackboard_id,scope_id,kind,actor_id,event_count,event_bytes)
 SELECT blackboard_id,COALESCE(milestone_id,''),'event',COALESCE(authenticated_actor->>'id',''),count(*),sum(octet_length(data::text))
 FROM plan_events WHERE source='agent'
 GROUP BY blackboard_id,COALESCE(milestone_id,''),authenticated_actor->>'id';
CREATE INDEX plan_proposals_actor_pending_idx ON plan_proposals(blackboard_id,(COALESCE(milestone_id,'')),(data->'actor'->>'id')) WHERE status='pending' AND data->'actor'->>'kind'='agent';
