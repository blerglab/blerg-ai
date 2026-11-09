-- Recover entry times from actual column transitions. If history is missing,
-- start conservatively at migration time rather than guessing from card age.
ALTER TABLE boards ADD COLUMN auto_archive_keep_tags text[] NOT NULL DEFAULT '{to-test}';
ALTER TABLE cards ADD COLUMN column_entered_at timestamptz;
UPDATE cards c SET column_entered_at = coalesce(
    (SELECT max(e.created_at) FROM card_events e WHERE e.card_id=c.id
        AND e.type IN ('moved','created') AND e.to_column_id=c.column_id
        AND e.from_column_id IS DISTINCT FROM e.to_column_id),
    now());
ALTER TABLE cards ALTER COLUMN column_entered_at SET DEFAULT now();
ALTER TABLE cards ALTER COLUMN column_entered_at SET NOT NULL;
CREATE FUNCTION track_card_column_entry() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' OR NEW.column_id IS DISTINCT FROM OLD.column_id THEN
        NEW.column_entered_at := now();
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER card_column_entry BEFORE INSERT OR UPDATE OF column_id ON cards
    FOR EACH ROW EXECUTE FUNCTION track_card_column_entry();
CREATE INDEX cards_live_column_entry ON cards(board_id,column_id,column_entered_at,id) WHERE archived_at IS NULL;
UPDATE boards SET auto_archive_done_days=0 WHERE coalesce(driven_by,'')<>'';
