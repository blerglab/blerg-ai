-- 073: children inherit board access (0.4.4). A session that starts another one can pass board
-- levels down (`blerg-runner session start --board <name|id>:<level>`), never above its own. The
-- child's level is stored like any session board level (a session_proposal_grants row of kind
-- board_file), with created_via 'parent'. Such a row has no sign-in of its own: every board call of
-- a child acts with its parent's sign-in for that board and is capped by the parent's level at that
-- moment, so proof_session_id may be empty for it.
ALTER TABLE session_proposal_grants DROP CONSTRAINT IF EXISTS session_proposal_grants_created_via_check;
ALTER TABLE session_proposal_grants ADD CONSTRAINT session_proposal_grants_created_via_check
  CHECK (created_via IN ('card','panel','launch','parent'));
ALTER TABLE session_proposal_grants DROP CONSTRAINT IF EXISTS session_proposal_grants_proof_session_id_check;
ALTER TABLE session_proposal_grants ADD CONSTRAINT session_proposal_grants_proof_session_id_check
  CHECK (proof_session_id <> '' OR (created_via = 'parent' AND kind = 'board_file'));
