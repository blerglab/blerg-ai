-- Blackboard identity is durable and deliberately has no session foreign key.
CREATE TABLE blackboards (
 id text PRIMARY KEY, owner_kind text NOT NULL, owner_id text NOT NULL,
 parent_id text REFERENCES blackboards(id), style jsonb NOT NULL DEFAULT '{}',
 version integer NOT NULL CHECK(version > 0), record jsonb NOT NULL
);
CREATE TABLE blackboard_links (
 blackboard_id text NOT NULL REFERENCES blackboards(id), workspace_id text NOT NULL,
 board_id text NOT NULL, PRIMARY KEY(blackboard_id, workspace_id, board_id)
);
CREATE TABLE goals (id text PRIMARY KEY, blackboard_id text NOT NULL REFERENCES blackboards(id), owner jsonb NOT NULL, version integer NOT NULL, data jsonb NOT NULL);
CREATE TABLE milestones (id text PRIMARY KEY, blackboard_id text NOT NULL REFERENCES blackboards(id), owner jsonb NOT NULL, board_id text NOT NULL, release_id text NOT NULL, target timestamptz, stage text NOT NULL, frozen_at timestamptz, version integer NOT NULL, data jsonb NOT NULL);
CREATE TABLE thrusts (id text PRIMARY KEY, blackboard_id text NOT NULL REFERENCES blackboards(id), milestone_id text NOT NULL REFERENCES milestones(id), goal_id text NOT NULL REFERENCES goals(id), owner jsonb NOT NULL, version integer NOT NULL, data jsonb NOT NULL);
CREATE TABLE plan_items (id text PRIMARY KEY, blackboard_id text NOT NULL REFERENCES blackboards(id), milestone_id text NOT NULL REFERENCES milestones(id), board_id text NOT NULL, card_number integer NOT NULL CHECK(card_number > 0), thrust_id text, reason text NOT NULL, raised_by jsonb NOT NULL, state text NOT NULL, data jsonb NOT NULL, UNIQUE(milestone_id,board_id,card_number));
CREATE TABLE plan_revisions (blackboard_id text NOT NULL REFERENCES blackboards(id), milestone_id text NOT NULL REFERENCES milestones(id), revision integer NOT NULL, data jsonb NOT NULL, PRIMARY KEY(milestone_id,revision));
CREATE TABLE plan_events (id text PRIMARY KEY, blackboard_id text NOT NULL REFERENCES blackboards(id), milestone_id text, authenticated_actor jsonb NOT NULL, recorded_at timestamptz NOT NULL, claimed_at timestamptz, source text NOT NULL, kind text NOT NULL, payload jsonb NOT NULL, data jsonb NOT NULL);
CREATE TABLE plan_proposals (id text PRIMARY KEY, blackboard_id text NOT NULL REFERENCES blackboards(id), milestone_id text NOT NULL REFERENCES milestones(id), revision integer NOT NULL, status text NOT NULL, data jsonb NOT NULL);
CREATE TABLE milestone_snapshots (blackboard_id text NOT NULL REFERENCES blackboards(id), milestone_id text NOT NULL REFERENCES milestones(id), day date NOT NULL, observed_at timestamptz NOT NULL, complete boolean NOT NULL, data jsonb NOT NULL, PRIMARY KEY(milestone_id,day));
CREATE FUNCTION blackboard_history_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'blackboard history is append-only'; END $$;
CREATE TRIGGER plan_events_immutable BEFORE UPDATE OR DELETE ON plan_events FOR EACH ROW EXECUTE FUNCTION blackboard_history_immutable();
CREATE TRIGGER plan_revisions_immutable BEFORE UPDATE OR DELETE ON plan_revisions FOR EACH ROW EXECUTE FUNCTION blackboard_history_immutable();
CREATE TRIGGER milestone_snapshots_immutable BEFORE UPDATE OR DELETE ON milestone_snapshots FOR EACH ROW EXECUTE FUNCTION blackboard_history_immutable();
