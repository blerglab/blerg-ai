CREATE TABLE IF NOT EXISTS update_notice_settings (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    enabled boolean NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by uuid
);
