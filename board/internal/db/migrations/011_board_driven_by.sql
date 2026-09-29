-- driven_by: names the external system that owns this board's card
-- lifecycle (e.g. "blerg-ops"), when one does. NULL = blerg-board-native board.
-- Replaces sniffing external_id prefixes (e.g. "INC-") to detect mirrored
-- boards — any external system can set this, not just the blerg-ops convention.
ALTER TABLE boards ADD COLUMN driven_by text;
