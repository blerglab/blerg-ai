-- Enrich the existing audit trail. This also covers tag curation and release rollover.
CREATE FUNCTION capture_burn_state() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.data = NEW.data || jsonb_build_object('burn_after', (
 SELECT jsonb_build_object('release',coalesce(c.release_id::text,''),'size',coalesce(c.size,''),
 'archived',c.archived_at IS NOT NULL,
 'done',(CASE WHEN c.archived_at IS NOT NULL THEN coalesce(
   (SELECT is_terminal FROM board_columns WHERE id=NEW.from_column_id),
   (SELECT (e.data->'burn_after'->>'done')::boolean FROM card_events e WHERE e.card_id=c.id AND e.data ? 'burn_after' ORDER BY e.id DESC LIMIT 1),
   (SELECT bc.is_terminal FROM card_events ae LEFT JOIN board_columns bc ON bc.id=ae.from_column_id WHERE ae.card_id=c.id AND ae.type='archived' ORDER BY ae.id DESC LIMIT 1), false)
   ELSE coalesce(col.is_terminal,false) END) OR EXISTS(SELECT 1 FROM release_done_cards d WHERE d.card_id=c.id AND d.release_id=c.release_id AND d.restored_at IS NULL),
 'tags',coalesce((SELECT jsonb_agg(tag ORDER BY tag) FROM card_tags t WHERE t.card_id=c.id),'[]'),
 'to_test',EXISTS(SELECT 1 FROM card_tags t WHERE t.card_id=c.id AND t.tag='to-test'))
 FROM cards c LEFT JOIN board_columns col ON col.id=c.column_id WHERE c.id=NEW.card_id));
 RETURN NEW;
END $$;
CREATE TRIGGER card_event_burn_state BEFORE INSERT ON card_events FOR EACH ROW EXECUTE FUNCTION capture_burn_state();
