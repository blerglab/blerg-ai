-- Historical identities must not regain old consent after an A -> B -> A change.
ALTER TABLE shell_host_consents ADD COLUMN revoked_at TIMESTAMPTZ;
