-- A blackboard and its plain items need neither a board nor intermediate levels.
-- ALTERs also upgrade a database initialized from the Task 2 interface handoff.
ALTER TABLE milestones ALTER COLUMN board_id DROP NOT NULL, ALTER COLUMN release_id DROP NOT NULL;
ALTER TABLE thrusts ALTER COLUMN milestone_id DROP NOT NULL;
ALTER TABLE plan_items ALTER COLUMN milestone_id DROP NOT NULL, ALTER COLUMN board_id DROP NOT NULL, ALTER COLUMN card_number DROP NOT NULL;
ALTER TABLE plan_proposals ALTER COLUMN milestone_id DROP NOT NULL;
ALTER TABLE plan_revisions ADD COLUMN IF NOT EXISTS scope_id text;
-- The table lock held by ALTER prevents application writes during structural backfill.
ALTER TABLE plan_revisions DISABLE TRIGGER plan_revisions_immutable;
UPDATE plan_revisions SET scope_id=coalesce(milestone_id,'') WHERE scope_id IS NULL;
ALTER TABLE plan_revisions ENABLE TRIGGER plan_revisions_immutable;
ALTER TABLE plan_revisions DROP CONSTRAINT IF EXISTS plan_revisions_pkey;
ALTER TABLE plan_revisions ALTER COLUMN milestone_id DROP NOT NULL, ALTER COLUMN scope_id SET NOT NULL;
ALTER TABLE plan_revisions ADD PRIMARY KEY(blackboard_id,scope_id,revision);
ALTER TABLE plan_items DROP CONSTRAINT IF EXISTS plan_items_milestone_id_board_id_card_number_key;
ALTER TABLE plan_items ADD CONSTRAINT plan_items_identity CHECK((board_id IS NULL AND card_number IS NULL) OR (board_id IS NOT NULL AND card_number IS NOT NULL AND card_number > 0));
CREATE UNIQUE INDEX IF NOT EXISTS plan_card_membership ON plan_items(blackboard_id,coalesce(milestone_id,''),board_id,card_number) WHERE board_id IS NOT NULL;
