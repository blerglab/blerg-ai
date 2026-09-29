-- Reversible deactivation (spec §3, ruling R2): a disabled account cannot log in
-- or refresh, and reconcile flips this both ways as org membership changes.
ALTER TABLE accounts ADD COLUMN disabled_at timestamptz;
