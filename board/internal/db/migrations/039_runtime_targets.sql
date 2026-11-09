-- The runner owns runtime_targets. This database stores its stable external ID.
ALTER TABLE boards ADD COLUMN runtime_target_id text CHECK (runtime_target_id='cluster');
UPDATE boards SET runtime_target_id='cluster' WHERE run_runtime = 'cluster';
ALTER TABLE runner_sessions ADD COLUMN runtime_target_id text CHECK (runtime_target_id='cluster');
UPDATE runner_sessions SET runtime_target_id='cluster' WHERE target_runtime = 'cluster';

CREATE FUNCTION fill_board_runtime_target() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.run_runtime = 'cluster' THEN
  NEW.runtime_target_id := COALESCE(NEW.runtime_target_id,'cluster');
 ELSE
  NEW.runtime_target_id := NULL;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER board_runtime_target BEFORE INSERT OR UPDATE ON boards
 FOR EACH ROW EXECUTE FUNCTION fill_board_runtime_target();
CREATE FUNCTION fill_board_session_runtime_target() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.target_runtime = 'cluster' THEN
  NEW.runtime_target_id := COALESCE(NEW.runtime_target_id,'cluster');
 ELSE
  NEW.runtime_target_id := NULL;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER board_session_runtime_target BEFORE INSERT OR UPDATE ON runner_sessions
 FOR EACH ROW EXECUTE FUNCTION fill_board_session_runtime_target();
