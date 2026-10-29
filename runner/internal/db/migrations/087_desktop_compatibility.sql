-- Snapshot upgrade-era machines and their historical people once. This grants
-- temporary compatibility access, never ownership or foreign desktop access.
ALTER TABLE daemons ADD COLUMN desktop_compatibility boolean NOT NULL DEFAULT false;
UPDATE daemons SET desktop_compatibility=true WHERE mode<>'runner' AND desktop_owner_id IS NULL;
CREATE TABLE desktop_compatibility_accounts (
 desktop_id uuid NOT NULL REFERENCES daemons(id) ON DELETE CASCADE,
 account_id text NOT NULL,
 PRIMARY KEY(desktop_id,account_id)
);
INSERT INTO desktop_compatibility_accounts(desktop_id,account_id)
 SELECT DISTINCT d.id,s.spawning_account_id FROM daemons d JOIN sessions s ON s.daemon_id=d.id
 WHERE d.desktop_compatibility AND s.spawning_account_id IS NOT NULL AND s.spawning_account_id<>'';
ALTER TABLE daemons ADD COLUMN enrollment_revision bigint NOT NULL DEFAULT 0;
ALTER TABLE daemons ADD COLUMN enrollment_owner_id text;
ALTER TABLE daemons ADD COLUMN enrollment_token_id text;
ALTER TABLE daemons ADD COLUMN enrollment_request_id uuid;
ALTER TABLE daemons ADD COLUMN enrollment_generation uuid;
CREATE TABLE desktop_enrollment_audit (
 id bigserial PRIMARY KEY,
 desktop_id uuid NOT NULL REFERENCES daemons(id),
 actor_id text NOT NULL,
 action text NOT NULL,
 ownership_revision bigint NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
