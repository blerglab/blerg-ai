-- 075: the session's plan stays honest by itself (0.4.5, card #158).
--   sessions.plan_nudges: whether Blerg tells the agent, on the next input, that its pins or checklist
--     drifted from what it is doing (on by default; the person turns it off in the session panel).
--   session_plan_nudges: what drifted, one row per fact (a key such as "closed:<board>#127"). A fact
--     is told once: nudged_at is set when a nudge carried it. pending holds the text of a fact found
--     at turn end or between turns, rechecked and attached to the next input.
--   child_result_reads: when a parent last read a child's result, and the child's newest event then,
--     so a child that has done nothing since can be called finished.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS plan_nudges boolean NOT NULL DEFAULT true;

CREATE TABLE IF NOT EXISTS session_plan_nudges (
  session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  key        text NOT NULL,
  pending    text,
  created_at timestamptz NOT NULL DEFAULT now(),
  nudged_at  timestamptz,
  PRIMARY KEY (session_id, key)
);

CREATE TABLE IF NOT EXISTS child_result_reads (
  parent_session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  child_session_id  uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  seq               bigint NOT NULL,
  read_at           timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (parent_session_id, child_session_id)
);

-- Keep the composite notice stable across retries of its carrying durable input. The input's
-- original frame stays unchanged, so client-id conflict checks still compare the person's input.
CREATE TABLE IF NOT EXISTS session_plan_deliveries (
  session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  client_id text NOT NULL,
  nudge text NOT NULL,
  PRIMARY KEY (session_id, client_id)
);
