ALTER TABLE accounts ADD COLUMN IF NOT EXISTS theme text NOT NULL DEFAULT ''
    CHECK (theme IN ('', 'system', 'dark', 'light', 'colorblind-dark', 'colorblind-light', 'high-contrast'));
