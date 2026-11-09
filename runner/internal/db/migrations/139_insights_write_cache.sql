-- Derived rows can be rebuilt without replaying history on a dashboard request.
ALTER TABLE session_insight_timings ADD COLUMN initial_rework boolean NOT NULL DEFAULT false;
ALTER TABLE session_insight_timings ADD COLUMN fingerprint_inputs jsonb NOT NULL DEFAULT '{}'::jsonb;
DELETE FROM session_insight_timings;
-- Marks affect only same-owner linked descendants whose work began afterwards.
CREATE FUNCTION invalidate_insight_card_mark() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE mark_at timestamptz;
BEGIN
 mark_at := CASE WHEN NEW.payload->>'at' ~ 'T.*(Z|[+-][0-9]{2}:[0-9]{2})$' AND pg_input_is_valid(NEW.payload->>'at','timestamp with time zone') THEN (NEW.payload->>'at')::timestamptz ELSE NEW.ts END;
 WITH RECURSIVE linked AS (
 SELECT s.id,s.spawning_account_id FROM sessions s JOIN session_card_links c ON c.session_id=s.id JOIN sessions author ON author.id=NEW.session_id
 WHERE s.spawning_account_id=author.spawning_account_id AND c.board_id=NEW.payload->>'board_id' AND c.card_number::text=NEW.payload->>'card'
 UNION SELECT s.id,s.spawning_account_id FROM sessions s JOIN linked p ON s.parent_session_id=p.id AND s.spawning_account_id=p.spawning_account_id WHERE NOT EXISTS(SELECT 1 FROM session_card_links c WHERE c.session_id=s.id)
 ), locked AS MATERIALIZED (
 SELECT s.id FROM sessions s JOIN linked l ON l.id=s.id WHERE s.started_at>=mark_at ORDER BY s.id FOR NO KEY UPDATE OF s
 ) DELETE FROM session_insight_timings c USING locked l WHERE c.session_id=l.id;
 RETURN NEW;
END;
$$;
CREATE TRIGGER invalidate_insight_card_mark AFTER INSERT ON agent_events FOR EACH ROW WHEN (NEW.kind='mark' AND NEW.payload->>'kind' IN ('review','failure')) EXECUTE FUNCTION invalidate_insight_card_mark();
CREATE INDEX sessions_insight_backfill_idx ON sessions(spawning_account_id,started_at DESC,id) WHERE ended_at IS NOT NULL;
-- Coordinate invalidation with materializers. UPDATE locks are shared with the materializer transaction, including
-- its cache insert and FK check.
CREATE OR REPLACE FUNCTION invalidate_insight_timing() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM id FROM sessions WHERE id=coalesce(NEW.session_id,OLD.session_id) AND ended_at IS NOT NULL FOR NO KEY UPDATE;
 DELETE FROM session_insight_timings c USING sessions s WHERE c.session_id=coalesce(NEW.session_id,OLD.session_id) AND s.id=c.session_id AND s.ended_at IS NOT NULL;
 RETURN coalesce(NEW,OLD);
END;
$$;

CREATE TRIGGER invalidate_insight_metric AFTER INSERT OR UPDATE OR DELETE ON workspace_metric_runs FOR EACH ROW EXECUTE FUNCTION invalidate_insight_timing();
CREATE FUNCTION invalidate_insight_session() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 DELETE FROM session_insight_timings WHERE session_id=NEW.id;
 RETURN NEW;
END;
$$;
CREATE TRIGGER invalidate_insight_session AFTER UPDATE OF started_at,ended_at,parent_session_id,spawning_account_id ON sessions FOR EACH ROW EXECUTE FUNCTION invalidate_insight_session();
-- A late pin/title/checklist link changes inherited stage evidence too.
CREATE FUNCTION invalidate_insight_links() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE sid uuid := coalesce(NEW.session_id,OLD.session_id);
BEGIN
 -- Mark invalidation has its own effective-time fence. A second source for
 -- the same link does not change membership.
 IF TG_OP='INSERT' THEN
  IF NEW.source IN ('review','failure') OR EXISTS(SELECT 1 FROM session_card_links c WHERE c.session_id=NEW.session_id AND c.board_id=NEW.board_id AND c.card_number=NEW.card_number AND c.source<>NEW.source) THEN RETURN NEW; END IF;
 END IF;
 WITH RECURSIVE affected AS (
 SELECT id,spawning_account_id FROM sessions WHERE id=sid
 UNION SELECT s.id,s.spawning_account_id FROM sessions s JOIN affected p ON s.parent_session_id=p.id AND s.spawning_account_id=p.spawning_account_id WHERE NOT EXISTS(SELECT 1 FROM session_card_links c WHERE c.session_id=s.id)
 ), locked AS MATERIALIZED (SELECT s.id FROM sessions s JOIN affected a ON a.id=s.id WHERE s.ended_at IS NOT NULL ORDER BY s.id FOR NO KEY UPDATE OF s)
 DELETE FROM session_insight_timings c USING locked a WHERE c.session_id=a.id;
 RETURN coalesce(NEW,OLD);
END;
$$;
CREATE TRIGGER invalidate_insight_links AFTER INSERT OR UPDATE OR DELETE ON session_card_links FOR EACH ROW EXECUTE FUNCTION invalidate_insight_links();
