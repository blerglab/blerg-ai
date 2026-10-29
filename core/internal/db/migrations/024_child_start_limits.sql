-- Idempotent: core records migration completion separately from applying it.
-- No row means environment-seeded defaults; only an administrator saves a row.
CREATE TABLE IF NOT EXISTS child_start_limits (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    parallel integer NOT NULL CHECK (parallel BETWEEN 1 AND 50),
    starts_per_hour integer NOT NULL CHECK (starts_per_hour BETWEEN 1 AND 10000),
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by uuid
);
