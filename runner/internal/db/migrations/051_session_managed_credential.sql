-- Private authorization for access-token delivery to the current Cluster pod.
-- This contains principal identifiers only, never provider credentials.
CREATE TABLE session_managed_credential (
 session_id uuid PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
 account_id text NOT NULL,
 engine text NOT NULL,
 token_id text NOT NULL DEFAULT '',
 auth_session_id text NOT NULL DEFAULT '',
 runtime_token_id uuid NOT NULL REFERENCES board_tokens(id) ON DELETE CASCADE,
 CHECK ((token_id <> '') <> (auth_session_id <> ''))
);
