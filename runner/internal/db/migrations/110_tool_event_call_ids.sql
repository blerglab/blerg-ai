-- Keep tool identities outside large result payloads so activity reads never
-- detoast terminal captures or file contents just to pair a result with a call.
-- 109 is reserved for routing work.
ALTER TABLE agent_events ADD COLUMN tool_call_id text;
UPDATE agent_events SET tool_call_id=payload->>'call_id' WHERE kind IN ('tool_call','tool_result');
CREATE FUNCTION cache_tool_call_id() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.tool_call_id := CASE WHEN NEW.kind IN ('tool_call','tool_result') THEN NEW.payload->>'call_id' ELSE NULL END;
 RETURN NEW;
END;
$$;
CREATE TRIGGER cache_tool_call_id BEFORE INSERT OR UPDATE OF payload,kind ON agent_events FOR EACH ROW EXECUTE FUNCTION cache_tool_call_id();
CREATE INDEX agent_events_timing_idx ON agent_events(session_id,seq DESC)
 WHERE kind IN ('tool_call','tool_result','status_changed','turn_done','mark');
