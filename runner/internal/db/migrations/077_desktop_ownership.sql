-- Existing machines remain unowned until the trusted installer/admin assignment
-- path runs. Names are display labels, never credential identities.
ALTER TABLE daemons ADD COLUMN desktop_owner_id text;
ALTER TABLE daemons ADD COLUMN ownership_revision bigint NOT NULL DEFAULT 1 CHECK (ownership_revision > 0);
ALTER TABLE daemons ADD COLUMN connection_generation uuid;
CREATE INDEX daemons_desktop_owner ON daemons(desktop_owner_id);
