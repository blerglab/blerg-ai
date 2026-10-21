-- 070: board access levels (0.4.4). A session's board permission (session_proposal_grants of kind
-- board_file) now carries a level, and each level includes the ones below it:
--   propose  (no row needed) search and propose filings that ask the person;
--   read     also list, get and search every card;
--   file     also file without asking (the 0.4.3 board filing permission);
--   full     also create, update, move, link and comment (what a board chosen at launch gives).
-- Every 0.4.3 filing permission becomes level 'file' (the column default). A change of level never
-- edits a row: the old row is revoked ('level_changed') and a new one is inserted, so the table is
-- the audit trail. A live 'propose' row exists only to lower a board chosen at launch, which has no
-- row of its own until the person changes it. An MCP tool permission keeps level 'file' (unused).
ALTER TABLE session_proposal_grants ADD COLUMN IF NOT EXISTS level text NOT NULL DEFAULT 'file'
  CONSTRAINT session_proposal_grants_level_check
  CHECK (level IN ('propose','read','file','full') AND (kind = 'board_file' OR level = 'file'));

-- The level a board has now is the latest row of (session, board): the gateway and the session's
-- board tools read it on every call.
CREATE INDEX IF NOT EXISTS session_proposal_grants_board_latest_idx
  ON session_proposal_grants (session_id, board_id, created_at DESC) WHERE kind = 'board_file';
