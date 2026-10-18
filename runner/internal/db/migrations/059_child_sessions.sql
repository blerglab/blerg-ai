-- 059: sessions start sessions (child sessions), approved by the person.
-- sessions.parent_session_id exists since 008 and was never written; children now carry it.
CREATE INDEX IF NOT EXISTS sessions_parent_session_id_idx ON sessions (parent_session_id)
  WHERE parent_session_id IS NOT NULL;
-- Deleting a parent must not fail on, or delete, its children: they keep their history rows.
-- (008 declared the FK with no ON DELETE action; recreate it. DROP IF EXISTS keeps this idempotent.)
ALTER TABLE sessions DROP CONSTRAINT IF EXISTS sessions_parent_session_id_fkey;
ALTER TABLE sessions ADD CONSTRAINT sessions_parent_session_id_fkey
  FOREIGN KEY (parent_session_id) REFERENCES sessions(id) ON DELETE SET NULL;
-- How a child was allowed: 'rule:<id>' | 'grant:<id>' | 'request:<id>'. NULL for every other session.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS delegation_via text;
-- A child started with keep_running survives its parent's end.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS keep_running boolean NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS delegation_requests (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  parent_session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  account_id        text NOT NULL,
  daemon_id         uuid NOT NULL,
  idempotency_key   text NOT NULL,
  request_hash      text NOT NULL,
  request           jsonb NOT NULL,      -- allowlisted body, full prompt, budget snapshot
  offered           text[] NOT NULL,     -- the choices the card may send (once|session|always|deny)
  status            text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','allowed','denied','expired')),
  choice            text CHECK (choice IN ('once','session','always')),
  child_session_id  uuid REFERENCES sessions(id) ON DELETE SET NULL,
  start_error       text,
  decided_by        text,
  decided_sid       text,
  created_at        timestamptz NOT NULL DEFAULT now(),
  decided_at        timestamptz,
  expires_at        timestamptz NOT NULL,
  UNIQUE (parent_session_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS delegation_requests_pending_idx ON delegation_requests (expires_at) WHERE status = 'pending';
-- At most one pending request per parent per identical request body (storage backstop for the server rule).
CREATE UNIQUE INDEX IF NOT EXISTS delegation_requests_pending_body_idx
  ON delegation_requests (parent_session_id, request_hash) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS delegation_requests_account_idx ON delegation_requests (account_id, created_at);

-- A completed start stays completed when ON DELETE SET NULL removes its child
-- reference. This also permits recovery of interrupted starts after long outages.
ALTER TABLE delegation_requests ADD COLUMN IF NOT EXISTS start_recorded_at timestamptz;
UPDATE delegation_requests SET start_recorded_at=COALESCE(decided_at,created_at)
WHERE start_recorded_at IS NULL AND (child_session_id IS NOT NULL OR start_error IS NOT NULL);

CREATE TABLE IF NOT EXISTS delegation_grants (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  parent_session_id    uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  account_id           text NOT NULL,
  engine               text NOT NULL,
  runtime              text NOT NULL CHECK (runtime IN ('docker','daemon')),
  daemon_id            uuid NOT NULL,
  repo                 text NOT NULL DEFAULT '',
  no_repo              boolean NOT NULL DEFAULT false,
  created_from_request uuid REFERENCES delegation_requests(id) ON DELETE SET NULL,
  created_at           timestamptz NOT NULL DEFAULT now(),
  revoked_at           timestamptz
);
CREATE INDEX IF NOT EXISTS delegation_grants_live_idx ON delegation_grants (parent_session_id) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS delegation_rules (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id  text NOT NULL,
  engine      text NOT NULL,
  runtime     text NOT NULL CHECK (runtime IN ('docker','daemon')),
  daemon_id   uuid NOT NULL REFERENCES daemons(id) ON DELETE CASCADE,
  local_only  boolean NOT NULL DEFAULT false,
  created_via text NOT NULL CHECK (created_via IN ('card','settings')),
  created_at  timestamptz NOT NULL DEFAULT now(),
  revoked_at  timestamptz
);
CREATE INDEX IF NOT EXISTS delegation_rules_live_idx ON delegation_rules (account_id) WHERE revoked_at IS NULL;
