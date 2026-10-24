-- 077 is reserved for the parallel desktop-ownership addition.
-- Root allocation is separate from immutable launch membership. Zero means no
-- runtime has been dispatched through this API; NULL binding means unavailable.
ALTER TABLE session_workspaces
 ADD COLUMN runtime_generation bigint NOT NULL DEFAULT 0 CHECK (runtime_generation >= 0),
 ADD COLUMN runtime_binding jsonb;
