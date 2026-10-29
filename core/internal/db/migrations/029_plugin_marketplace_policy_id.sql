-- The policy identity survives Core restarts and changes when the singleton is
-- recreated. Rotate it after restoring a backup that already contains this ID.
ALTER TABLE plugin_marketplace_policy
 ADD COLUMN IF NOT EXISTS policy_id uuid NOT NULL DEFAULT gen_random_uuid();
