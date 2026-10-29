-- Install-wide attachment storage for sessions. No row means environment-seeded defaults
-- (BLERG_RUNNER_MAX_SESSION_UPLOADS_MB); only an administrator saves a row, and every save is
-- kept in session_storage_limits_audit. Idempotent: core records completion separately.
CREATE TABLE IF NOT EXISTS session_storage_limits (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    default_upload_mb bigint NOT NULL CHECK (default_upload_mb BETWEEN 1 AND 1048576),
    max_upload_mb bigint NOT NULL CHECK (max_upload_mb BETWEEN 1 AND 1048576 AND max_upload_mb >= default_upload_mb),
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by uuid
);
CREATE TABLE IF NOT EXISTS session_storage_limits_audit (
    id bigserial PRIMARY KEY,
    actor uuid NOT NULL,
    previous_default_mb bigint NOT NULL,
    previous_max_mb bigint NOT NULL,
    default_upload_mb bigint NOT NULL,
    max_upload_mb bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
