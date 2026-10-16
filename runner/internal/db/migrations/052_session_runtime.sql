-- Current launch authority. Tokens contain no provider credentials; replacement
-- rotates both capabilities and permanently revokes the previous launch.
CREATE TABLE session_runtime (
 repo text NOT NULL,
 project_path text NOT NULL,
 daemon_id uuid NOT NULL DEFAULT gen_random_uuid(),
 session_id uuid PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
 runtime_token_id uuid NOT NULL UNIQUE REFERENCES board_tokens(id) ON DELETE CASCADE,
 messaging_token_id uuid NOT NULL UNIQUE REFERENCES board_tokens(id) ON DELETE CASCADE
);
