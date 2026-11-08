-- Estimates are available on every board, independent of its custom field schema.
-- Historical timestamps are unknown: never infer them from updated_at.
ALTER TABLE cards
 ADD COLUMN risk text CHECK (risk IN ('low','medium','high')),
 ADD COLUMN complexity text CHECK (complexity IN ('low','medium','high')),
 ADD COLUMN size_first_set_at timestamptz,
 ADD COLUMN risk_first_set_at timestamptz,
 ADD COLUMN complexity_first_set_at timestamptz,
 ADD COLUMN size_first_set_recorded boolean NOT NULL DEFAULT false;
-- A legacy estimate was set at an unknown time. Preserve that uncertainty forever,
-- including across clearing and re-estimation.
UPDATE cards SET size_first_set_recorded=true WHERE NULLIF(size,'') IS NOT NULL;

CREATE FUNCTION card_estimate_first_set() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'INSERT' THEN
  NEW.size_first_set_recorded := NULLIF(NEW.size,'') IS NOT NULL;
  IF NEW.size_first_set_recorded THEN NEW.size_first_set_at := now(); END IF;
  IF NEW.risk IS NOT NULL THEN NEW.risk_first_set_at := now(); END IF;
  IF NEW.complexity IS NOT NULL THEN NEW.complexity_first_set_at := now(); END IF;
 ELSE
  NEW.size_first_set_at := OLD.size_first_set_at;
  NEW.size_first_set_recorded := OLD.size_first_set_recorded;
  NEW.risk_first_set_at := OLD.risk_first_set_at;
  NEW.complexity_first_set_at := OLD.complexity_first_set_at;
  IF NOT OLD.size_first_set_recorded AND NULLIF(NEW.size,'') IS NOT NULL THEN NEW.size_first_set_at := now(); NEW.size_first_set_recorded := true; END IF;
  IF NEW.risk_first_set_at IS NULL AND NEW.risk IS NOT NULL AND NEW.risk IS DISTINCT FROM OLD.risk THEN NEW.risk_first_set_at := now(); END IF;
  IF NEW.complexity_first_set_at IS NULL AND NEW.complexity IS NOT NULL AND NEW.complexity IS DISTINCT FROM OLD.complexity THEN NEW.complexity_first_set_at := now(); END IF;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER card_estimate_first_set BEFORE INSERT OR UPDATE ON cards FOR EACH ROW EXECUTE FUNCTION card_estimate_first_set();

-- Snapshot column meaning at entry. A later rename/delete must not rewrite flow history.
CREATE FUNCTION card_flow_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE col board_columns%ROWTYPE;
BEGIN
 IF NEW.type IN ('created','moved') AND NEW.to_column_id IS NOT NULL AND NEW.to_column_id IS DISTINCT FROM NEW.from_column_id THEN
  SELECT * INTO col FROM board_columns WHERE id=NEW.to_column_id;
  NEW.data := NEW.data || jsonb_build_object('flow', jsonb_build_object(
   'column_name', col.name,
   'in_progress', lower(col.name) LIKE '%progress%' OR lower(col.name) LIKE '%doing%' OR lower(col.name) LIKE '%working%',
   'done', col.is_terminal OR lower(col.name) LIKE '%done%'));
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER card_flow_snapshot BEFORE INSERT ON card_events FOR EACH ROW EXECUTE FUNCTION card_flow_snapshot();
