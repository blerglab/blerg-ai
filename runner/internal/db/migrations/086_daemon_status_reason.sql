-- Socket status is independent of the session recovery grace period.
ALTER TABLE daemons ADD COLUMN status_reason text NOT NULL DEFAULT '';
