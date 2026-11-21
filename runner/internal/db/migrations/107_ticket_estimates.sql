-- Estimates are available on every board, independent of its custom field schema.
-- Historical timestamps are unknown: never infer them from updated_at.
ALTER TABLE tickets
 ADD COLUMN risk text CHECK (risk IN ('low','medium','high')),
 ADD COLUMN complexity text CHECK (complexity IN ('low','medium','high')),
 ADD COLUMN size_first_set_at timestamptz,
 ADD COLUMN risk_first_set_at timestamptz,
 ADD COLUMN complexity_first_set_at timestamptz,
 ADD COLUMN size_first_set_recorded boolean NOT NULL DEFAULT false;
-- A legacy estimate was set at an unknown time. Preserve that uncertainty forever,
-- including across clearing and re-estimation.
UPDATE tickets SET size_first_set_recorded=true WHERE NULLIF(size,'') IS NOT NULL;

CREATE FUNCTION ticket_estimate_first_set() RETURNS trigger LANGUAGE plpgsql AS $$
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
CREATE TRIGGER ticket_estimate_first_set BEFORE INSERT OR UPDATE ON tickets FOR EACH ROW EXECUTE FUNCTION ticket_estimate_first_set();

