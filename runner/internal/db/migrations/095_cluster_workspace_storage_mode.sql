ALTER TABLE cluster_workspace_storage
 ADD COLUMN storage_mode text NOT NULL DEFAULT 'bounded'
 CHECK (storage_mode IN ('bounded','standard'));
