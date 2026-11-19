-- Constant defaults mark history pending without replaying events or holding
-- write locks on agent_events. The worker claims at most fifty rows per batch.
ALTER TABLE sessions ADD COLUMN insight_pending boolean NOT NULL DEFAULT true;
ALTER TABLE sessions ADD COLUMN insight_attempts integer NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN insight_retry_at timestamptz NOT NULL DEFAULT now();
CREATE INDEX sessions_insight_pending_idx ON sessions(spawning_account_id,started_at DESC,id) INCLUDE(insight_retry_at) WHERE insight_pending AND ended_at IS NOT NULL;
CREATE FUNCTION insight_cache_ready() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE sessions SET insight_pending=(coalesce(NEW.fingerprint_inputs->>'version','')<>'5'),insight_attempts=0 WHERE id=NEW.session_id AND ended_at IS NOT NULL;
 RETURN NEW;
END;
$$;
CREATE TRIGGER insight_cache_ready AFTER INSERT OR UPDATE ON session_insight_timings FOR EACH ROW EXECUTE FUNCTION insight_cache_ready();
CREATE FUNCTION insight_cache_pending() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE sessions SET insight_pending=true,insight_attempts=0,insight_retry_at=now() WHERE id=OLD.session_id;
 RETURN OLD;
END;
$$;
CREATE TRIGGER insight_cache_pending AFTER DELETE ON session_insight_timings FOR EACH ROW EXECUTE FUNCTION insight_cache_pending();
CREATE FUNCTION insight_session_pending() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.insight_pending=true;
 NEW.insight_attempts=0;
 NEW.insight_retry_at=now();
 RETURN NEW;
END;
$$;
CREATE TRIGGER insight_session_pending BEFORE UPDATE OF started_at,ended_at,parent_session_id,spawning_account_id ON sessions FOR EACH ROW EXECUTE FUNCTION insight_session_pending();
