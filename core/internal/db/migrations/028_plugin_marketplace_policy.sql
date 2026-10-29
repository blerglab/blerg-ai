-- Seeded once by Core from the operator's initial environment, after migration.
CREATE TABLE IF NOT EXISTS plugin_marketplace_policy (
 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
 revision bigint NOT NULL CHECK (revision > 0),
 marketplaces jsonb NOT NULL,
 any_marketplace boolean NOT NULL DEFAULT false,
 updated_by uuid,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS plugin_marketplace_audit (
 revision bigint PRIMARY KEY,
 actor_id uuid,
 login_id uuid,
 changed_at timestamptz NOT NULL DEFAULT now(),
 before_policy jsonb,
 after_policy jsonb NOT NULL
);
