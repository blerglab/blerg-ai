-- Retained control-plane delivery records contain no provider credentials. No
-- sessions FK: deletion must first leave a durable closing record for Core.
CREATE TABLE IF NOT EXISTS mcp_session_bindings (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 session_id uuid NOT NULL,
 connection_id uuid NOT NULL,
 account_id text NOT NULL,
 scope jsonb NOT NULL,
 control_generation bigint NOT NULL CHECK(control_generation>0),
 terminal_record jsonb CHECK(terminal_record IS NULL OR (jsonb_typeof(terminal_record)='array' AND jsonb_array_length(terminal_record)<=32)),
 retained_grant jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(retained_grant)='object'),
 desired_action text NOT NULL DEFAULT 'active' CHECK(desired_action IN ('active','suspend','close','done','superseded')),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 delivered boolean NOT NULL DEFAULT false,
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(session_id,connection_id,scope),
 CHECK(jsonb_typeof(scope)='object'
  AND scope->>'runner_session_id' IS NOT NULL AND scope->>'runner_session_id'=session_id::text
  AND scope->>'account_id' IS NOT NULL AND scope->>'account_id'=account_id
  AND scope->>'runner_installation_id' IS NOT NULL AND length(scope->>'runner_installation_id') BETWEEN 1 AND 128
  AND scope->>'human_session_id' IS NOT NULL AND length(scope->>'human_session_id')=36
  AND scope->>'launch_generation' IS NOT NULL AND (scope->>'launch_generation')::bigint>0
  AND scope->>'credential_generation' IS NOT NULL AND (scope->>'credential_generation')::bigint>0
  AND control_generation BETWEEN (scope->>'credential_generation')::bigint AND (scope->>'credential_generation')::bigint+1
  AND scope->>'grant_token_hash' IS NOT NULL AND octet_length(decode(scope->>'grant_token_hash','hex'))=32)
);
CREATE INDEX IF NOT EXISTS mcp_session_bindings_pending ON mcp_session_bindings(updated_at) WHERE desired_action IN ('active','suspend','close');

CREATE OR REPLACE FUNCTION mcp_binding_grant_record() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.session_scope IS NOT NULL THEN
   INSERT INTO mcp_session_bindings(session_id,connection_id,account_id,scope,control_generation,retained_grant)
    VALUES(NEW.session_id,NEW.connection_id,NEW.account_id,NEW.session_scope,(NEW.session_scope->>'credential_generation')::bigint,
      jsonb_build_object('name',NEW.name,'url',NEW.url_snapshot,'tools',NEW.tools,'preset',NEW.preset,'known_tools',NEW.known_tools,'call_budget',NEW.call_budget,'calls_used',NEW.calls_used)) ON CONFLICT DO NOTHING;
  END IF;
  RETURN NEW;
 END IF;
 IF OLD.session_scope IS NOT NULL THEN
  UPDATE mcp_session_bindings SET
   retained_grant=jsonb_build_object('name',OLD.name,'url',OLD.url_snapshot,'tools',OLD.tools,'preset',OLD.preset,'known_tools',OLD.known_tools,'call_budget',OLD.call_budget,'calls_used',OLD.calls_used),
   desired_action=CASE WHEN desired_action='active' THEN 'suspend' ELSE desired_action END,
   delivered=CASE WHEN desired_action='active' THEN false ELSE delivered END,revision=revision+1,updated_at=clock_timestamp()
   WHERE session_id=OLD.session_id AND connection_id=OLD.connection_id AND scope=OLD.session_scope;
 END IF;
 RETURN OLD;
END $$;
DROP TRIGGER IF EXISTS mcp_binding_grant_insert ON session_mcp_grants;
CREATE TRIGGER mcp_binding_grant_insert AFTER INSERT ON session_mcp_grants FOR EACH ROW EXECUTE FUNCTION mcp_binding_grant_record();
DROP TRIGGER IF EXISTS mcp_binding_grant_delete ON session_mcp_grants;
CREATE TRIGGER mcp_binding_grant_delete AFTER DELETE ON session_mcp_grants FOR EACH ROW EXECUTE FUNCTION mcp_binding_grant_record();

-- Retention classes, not a terminality decision. Go alone authorizes closing.
-- Preserve every input class of TerminallyEnded (ended_at is always non-null).
-- The contract test covers the complete Go reason vocabulary and status classes.
CREATE OR REPLACE FUNCTION mcp_binding_fact_reasons() RETURNS text[] LANGUAGE sql IMMUTABLE AS $$
 SELECT ARRAY['stopped_by_user','stopped_by_agent','auto_stopped','process_exited','process_failed','start_failed','job_finished','job_failed','job_disappeared','not_resumed','daemon_lost','daemon_unreported','parent_ended']::text[]
$$;

CREATE OR REPLACE FUNCTION mcp_binding_fact_class(f jsonb) RETURNS text LANGUAGE sql IMMUTABLE AS $$
 SELECT (CASE WHEN f->>'end_reason'=ANY(mcp_binding_fact_reasons())
 THEN f->>'end_reason' ELSE '<unknown>' END) || ':' || ((coalesce(f->>'status','')='stopped')::text)
$$;

CREATE OR REPLACE FUNCTION mcp_binding_runtime_record() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE terminal boolean; detach boolean; relaunch boolean;
BEGIN
 IF NOT EXISTS(SELECT 1 FROM mcp_session_bindings WHERE session_id=OLD.id AND desired_action<>'done') AND NOT EXISTS(SELECT 1 FROM session_mcp_grants WHERE session_id=OLD.id AND session_scope IS NOT NULL) THEN
  IF TG_OP='DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
 END IF;
 IF TG_OP='DELETE' THEN terminal:=true;detach:=true;relaunch:=false;
 ELSE
  terminal:=NEW.discard_requested_at IS NOT NULL OR NEW.delegation_ended_at IS NOT NULL;
  IF NEW.ended_at IS NOT NULL AND OLD.ended_at IS DISTINCT FROM NEW.ended_at THEN
   -- Retain conversation terminal facts, including bindings from older launches.
   -- Go uses canonical TerminallyEnded; each Core close keeps its own fence.
   UPDATE mcp_session_bindings SET terminal_record=(SELECT jsonb_agg(f ORDER BY ord) FROM (
      SELECT DISTINCT ON (mcp_binding_fact_class(f)) f,ord FROM jsonb_array_elements(
       coalesce(terminal_record,'[]'::jsonb)||jsonb_build_array(jsonb_build_object('status',NEW.status,'ended_at',NEW.ended_at,'end_reason',NEW.end_reason))) WITH ORDINALITY AS facts(f,ord)
      ORDER BY mcp_binding_fact_class(f),ord DESC) retained),revision=revision+1,delivered=false,updated_at=clock_timestamp()
    WHERE session_id=OLD.id AND desired_action IN ('active','suspend','superseded');
  END IF;
  relaunch:=(NEW.status='starting' AND OLD.status<>'starting') OR
   (NEW.status IN ('running','idle','waiting') AND OLD.status NOT IN ('starting','running','idle','waiting')) OR
   (coalesce(OLD.paused_reason,'')<>'' AND coalesce(NEW.paused_reason,'')='');
  detach:=terminal OR NEW.ended_at IS NOT NULL OR relaunch OR NEW.status NOT IN ('running','idle','waiting') OR coalesce(NEW.paused_reason,'')<>'' OR NEW.mcp_launch_generation<>OLD.mcp_launch_generation;
  IF relaunch THEN NEW.mcp_launch_generation:=OLD.mcp_launch_generation+1; END IF;
 END IF;
 IF terminal THEN
  UPDATE mcp_session_bindings SET desired_action='close',delivered=false,revision=revision+1,updated_at=clock_timestamp()
   WHERE session_id=OLD.id AND desired_action IN ('active','suspend','superseded');
 ELSIF detach THEN
  UPDATE mcp_session_bindings SET desired_action='suspend',delivered=false,revision=revision+1,updated_at=clock_timestamp()
   WHERE session_id=OLD.id AND desired_action='active';
 END IF;
 IF detach THEN DELETE FROM session_mcp_grants WHERE session_id=OLD.id AND session_scope IS NOT NULL; END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS mcp_binding_runtime_update ON sessions;
CREATE TRIGGER mcp_binding_runtime_update BEFORE UPDATE OF status,ended_at,end_reason,paused_reason,discard_requested_at,delegation_ended_at,mcp_launch_generation ON sessions FOR EACH ROW EXECUTE FUNCTION mcp_binding_runtime_record();
DROP TRIGGER IF EXISTS mcp_binding_runtime_delete ON sessions;
CREATE TRIGGER mcp_binding_runtime_delete BEFORE DELETE ON sessions FOR EACH ROW EXECUTE FUNCTION mcp_binding_runtime_record();

-- Adopt any staged scoped rows already present before lifecycle wiring.
INSERT INTO mcp_session_bindings(session_id,connection_id,account_id,scope,control_generation,retained_grant)
 SELECT session_id,connection_id,account_id,session_scope,(session_scope->>'credential_generation')::bigint,
  jsonb_build_object('name',name,'url',url_snapshot,'tools',tools,'preset',preset,'known_tools',known_tools,'call_budget',call_budget,'calls_used',calls_used)
 FROM session_mcp_grants WHERE session_scope IS NOT NULL ON CONFLICT DO NOTHING;
