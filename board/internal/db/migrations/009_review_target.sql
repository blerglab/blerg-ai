-- Held update/move/archive/delete reviews need to remember which card they
-- targeted and at what version, so approve can replay against the current
-- row and detect drift since the hold. NULL for create (no target) and for
-- rows written before this migration.
--
-- Deliberately NOT a foreign key: approve must be able to tell "the target
-- was deleted since the hold" apart from "no target was ever recorded", and
-- a REFERENCES ... ON DELETE SET NULL would erase that exact evidence the
-- moment the card is deleted.
ALTER TABLE admission_reviews ADD COLUMN target_card_id uuid;
ALTER TABLE admission_reviews ADD COLUMN target_version integer;
