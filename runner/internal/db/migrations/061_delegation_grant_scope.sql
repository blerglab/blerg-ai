-- Session delegation covers every repository on the approved engine/runtime/daemon.
-- Keep the original repository and all historical grant IDs as audit records.
-- Obsolete lifetimes must not occupy the unique live scope during delayed cleanup.
UPDATE delegation_grants g SET revoked_at=now()
FROM sessions p WHERE p.id=g.parent_session_id AND g.revoked_at IS NULL
  AND (g.created_at <= p.delegation_ended_at
    OR g.account_id IS DISTINCT FROM p.spawning_account_id);

WITH ranked AS (
  SELECT id, row_number() OVER (
    PARTITION BY parent_session_id, engine, runtime, daemon_id
    ORDER BY created_at, id
  ) AS position
  FROM delegation_grants WHERE revoked_at IS NULL
)
UPDATE delegation_grants g SET revoked_at=now()
FROM ranked r WHERE g.id=r.id AND r.position > 1;

CREATE UNIQUE INDEX IF NOT EXISTS delegation_grants_live_scope_idx
  ON delegation_grants (parent_session_id, engine, runtime, daemon_id)
  WHERE revoked_at IS NULL;
