-- 019: runner_settings — operator-tunable runtime settings an admin can change
-- from the UI without a redeploy (cluster pod idle timeout, pod lifetime cap).
-- A row overrides the environment default; deleting it restores the default.
CREATE TABLE IF NOT EXISTS runner_settings (
    key        text PRIMARY KEY,
    value      bigint NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by text NOT NULL DEFAULT ''
);
