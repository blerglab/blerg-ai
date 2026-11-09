-- An explicit administrator disable must outlive organization membership reconciliation.
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS admin_disabled boolean NOT NULL DEFAULT false
 CHECK (NOT admin_disabled OR disabled_at IS NOT NULL);
