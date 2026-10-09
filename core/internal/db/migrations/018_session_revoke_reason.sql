-- Why a browser session row was revoked, for the operator log and a future signed-in-devices
-- page: 'rotated' (the normal refresh), 'superseded' (a rotation the browser never received was
-- redone), 'reuse' (a rotated-out token was presented again after its successor had been used:
-- the chain was revoked), 'logout', 'logout_all'. NULL for rows revoked before this column
-- existed.
ALTER TABLE human_sessions ADD COLUMN IF NOT EXISTS revoke_reason text;

-- last_used_at now means "this token was presented": a freshly issued successor row has none
-- until the browser uses it, which is how a lost rotation is told from a reused token.
