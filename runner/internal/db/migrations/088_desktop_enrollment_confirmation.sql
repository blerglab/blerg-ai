-- Correlate a person's explicit confirmation and each outcome with the exact
-- displayed socket. Display details and credential material are never stored here.
ALTER TABLE desktop_enrollment_audit ADD COLUMN connection_generation uuid;
