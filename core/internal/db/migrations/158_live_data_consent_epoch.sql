ALTER TABLE live_data_owner_settings ADD COLUMN IF NOT EXISTS consent_epoch bigint NOT NULL DEFAULT 0 CHECK(consent_epoch>=0);
