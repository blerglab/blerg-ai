-- A card can link a file a runner session published (an "artifact"): the link is the runner's viewer URL for
-- that file, validated by the board (db.validateLinks).
ALTER TABLE card_links DROP CONSTRAINT IF EXISTS card_links_kind_check;
ALTER TABLE card_links
    ADD CONSTRAINT card_links_kind_check CHECK (kind IN ('session','pr','rcca','doc','url','artifact'));
