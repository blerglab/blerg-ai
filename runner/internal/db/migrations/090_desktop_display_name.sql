-- A person's own name for a desktop (Settings → Account → Your desktops → Rename). The daemon
-- reports its host name on every connection into `name`; this one is never overwritten by it.
ALTER TABLE daemons ADD COLUMN IF NOT EXISTS display_name text NOT NULL DEFAULT '';
