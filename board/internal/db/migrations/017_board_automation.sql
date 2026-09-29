-- The identity a board starts runner sessions under, and the engine they run.
--
-- Before this, every session blerg-board started (Run board, quiet-worker
-- respawn, reviewer spawn, card Run, board chat, standing agents) went to the
-- runner under the shared BLERG_RUNNER_KEY: a principal with no account, so a
-- cluster session could only ever use the operator's shared engine credential.
-- Credentials are self-service (each person connects their own in blerg-core
-- Settings), so there is no operator credential to fall back to and such a
-- session dies at start.
--
-- automation_token is a blerg-core agent token (preset run-sessions) that a
-- board admin minted for THEMSELVES and pasted into the board: the runner
-- resolves it to its owner's account and fetches that person's own engine
-- credential, exactly as it does for any other agent-token caller. It is a
-- bearer credential, so it is write-only at the API: no endpoint ever returns
-- it (db.Board has no field for it; db.BoardAutomation is the one reader).
-- '' = not configured, and the board refuses to start sessions until it is.
--
-- automation_token_expires_at is the token's own exp claim, recorded when it
-- was saved, so the UI can say "expires in 3 days" without ever reading the
-- token back.
--
-- automation_engine is the engine those sessions run: claude, codex or hermes.
ALTER TABLE boards
    ADD COLUMN automation_token text NOT NULL DEFAULT '',
    ADD COLUMN automation_token_expires_at timestamptz,
    ADD COLUMN automation_engine text NOT NULL DEFAULT 'claude'
        CHECK (automation_engine IN ('claude', 'codex', 'hermes'));
