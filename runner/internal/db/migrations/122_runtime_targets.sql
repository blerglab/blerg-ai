-- This release names the existing cluster; it does not add another executor.
-- RunMigrations applies the seed, backfill and compatibility triggers atomically.
CREATE TABLE runtime_targets (
 id text PRIMARY KEY,
 kind text NOT NULL CHECK (kind = 'cluster'),
 name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
 config jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(config) = 'object'),
 is_default boolean NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX runtime_targets_single_default ON runtime_targets(is_default) WHERE is_default;
INSERT INTO runtime_targets(id,kind,name,is_default) VALUES ('cluster','cluster','Cluster',true);

ALTER TABLE sessions ADD COLUMN runtime_target_id text REFERENCES runtime_targets(id);
UPDATE sessions SET runtime_target_id='cluster' WHERE runtime='cluster';
ALTER TABLE crons ADD COLUMN runtime_target_id text REFERENCES runtime_targets(id);
UPDATE crons SET runtime_target_id='cluster' WHERE runtime = 'cluster';
ALTER TABLE cluster_workspace_storage ADD COLUMN runtime_target_id text NOT NULL DEFAULT 'cluster' REFERENCES runtime_targets(id);
ALTER TABLE shell_names ADD COLUMN runtime_target_id text REFERENCES runtime_targets(id);
UPDATE shell_names n SET runtime_target_id=s.runtime_target_id FROM sessions s WHERE s.id=n.session_id;

-- Old servers and daemons may still insert or update posture without a target.
CREATE FUNCTION fill_cluster_runtime_target() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.runtime = 'cluster' THEN
  NEW.runtime_target_id := COALESCE(NULLIF(NEW.runtime_target_id,''), (SELECT id FROM runtime_targets WHERE is_default));
 ELSE
  NEW.runtime_target_id := NULL;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER session_runtime_target BEFORE INSERT OR UPDATE ON sessions
 FOR EACH ROW EXECUTE FUNCTION fill_cluster_runtime_target();
CREATE TRIGGER cron_runtime_target BEFORE INSERT OR UPDATE ON crons
 FOR EACH ROW EXECUTE FUNCTION fill_cluster_runtime_target();

CREATE FUNCTION fill_shell_runtime_target() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 SELECT runtime_target_id INTO NEW.runtime_target_id FROM sessions WHERE id=NEW.session_id;
 RETURN NEW;
END $$;
CREATE TRIGGER shell_runtime_target BEFORE INSERT OR UPDATE ON shell_names
 FOR EACH ROW EXECUTE FUNCTION fill_shell_runtime_target();

-- There is exactly one default, including installations with no sessions yet.
CREATE FUNCTION retain_default_runtime_target() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'TRUNCATE' THEN
  RAISE EXCEPTION 'the default runtime target cannot be removed';
 END IF;
 IF OLD.is_default AND (TG_OP = 'DELETE' OR NOT NEW.is_default OR NEW.id <> OLD.id) THEN
  RAISE EXCEPTION 'the default runtime target cannot be removed';
 END IF;
 IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER retain_default_runtime_target BEFORE DELETE OR UPDATE ON runtime_targets
 FOR EACH ROW EXECUTE FUNCTION retain_default_runtime_target();
CREATE TRIGGER retain_default_runtime_target_on_truncate BEFORE TRUNCATE ON runtime_targets
 FOR EACH STATEMENT EXECUTE FUNCTION retain_default_runtime_target();

ALTER TABLE session_storage_audit ADD COLUMN runtime_target_id text REFERENCES runtime_targets(id);
UPDATE session_storage_audit a SET runtime_target_id=s.runtime_target_id FROM sessions s WHERE s.id=a.session_id;
CREATE TRIGGER storage_audit_runtime_target BEFORE INSERT ON session_storage_audit
 FOR EACH ROW EXECUTE FUNCTION fill_shell_runtime_target();
ALTER TABLE session_file_deletions ADD COLUMN runtime_target_id text REFERENCES runtime_targets(id);
UPDATE session_file_deletions a SET runtime_target_id=s.runtime_target_id FROM sessions s WHERE s.id=a.session_id;
CREATE TRIGGER file_deletion_runtime_target BEFORE INSERT ON session_file_deletions
 FOR EACH ROW EXECUTE FUNCTION fill_shell_runtime_target();
CREATE INDEX session_storage_audit_target ON session_storage_audit(runtime_target_id);
CREATE INDEX session_file_deletions_target ON session_file_deletions(runtime_target_id);
