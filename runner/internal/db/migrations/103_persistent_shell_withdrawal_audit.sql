-- Denial and its audit commit together. This metadata never authorizes access.
-- Retention follows the existing accepted-consent tombstone.
ALTER TABLE shell_host_consents_v2 ADD COLUMN withdrawal_audit jsonb;
