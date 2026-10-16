-- Expected native state is distinct from legacy sessions with no durable native
-- history. Losing an expected claim must never create an empty replacement.
CREATE TABLE session_native_history (
 session_id uuid PRIMARY KEY,
 session_ref uuid UNIQUE REFERENCES sessions(id) ON DELETE SET NULL,
 account_id text NOT NULL,
 engine text NOT NULL,
 claim_name text NOT NULL UNIQUE,
 claim_uid text NOT NULL UNIQUE,
 created_at timestamptz NOT NULL DEFAULT now(),
 last_used_at timestamptz NOT NULL DEFAULT now(),
 reclaimed_at timestamptz,
 detached_at timestamptz
);

-- Preserve the original session/claim identity and start deletion retention
-- when the referenced session disappears. FK enforcement serializes binding
-- against deletion; a detached tombstone is never reattached to a new row.
CREATE FUNCTION stamp_native_history_detached() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.session_ref IS NOT NULL AND NEW.session_ref IS NULL THEN
  NEW.detached_at := now();
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER session_native_history_detached
 BEFORE UPDATE OF session_ref ON session_native_history
 FOR EACH ROW EXECUTE FUNCTION stamp_native_history_detached();
