-- 068: a session that must never run on operator-supplied material (the operator Secret's
-- credentials, or the cluster's shared Hermes providers), recorded on the row so every resume and
-- message path keeps it. Set for a cluster child of a cron session; a cron's own session is
-- recognised by its cron_id. false for every other session, and for rows from before.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS no_operator_fallback boolean NOT NULL DEFAULT false;
