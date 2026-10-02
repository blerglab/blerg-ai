-- Exchange tokens issued to built-in board grants (AI crons spec 10.2), remembered durably so a
-- runner crash, restart or another replica can still revoke them at core when the session ends.
-- One row per issued token, keyed by its `sub` (the id core revokes it by). token_hash names the
-- grant it was issued for: once no session_mcp_grants row carries that hash, the grant is gone and
-- the token is owed a revocation. Rows are deleted when revoked, or when the token has expired on
-- its own (<= 10 minutes).
CREATE TABLE board_exchange_subs (
  sub         text PRIMARY KEY,
  token_hash  bytea NOT NULL,
  session_id  text NOT NULL,
  account_id  text NOT NULL,
  expires_at  timestamptz NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX board_exchange_subs_token_hash ON board_exchange_subs (token_hash);
CREATE INDEX board_exchange_subs_expires ON board_exchange_subs (expires_at);
