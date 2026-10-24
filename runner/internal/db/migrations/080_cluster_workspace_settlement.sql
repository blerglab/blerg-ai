-- P3-private durable final settlement. A matching generation means outcomes
-- are sealed and only Job/terminal cleanup remains; replacement generations
-- cannot reuse the marker. Keep it with the retained storage evidence.
ALTER TABLE cluster_workspace_storage
 ADD COLUMN settled_generation bigint NOT NULL DEFAULT 0 CHECK (settled_generation >= 0);
