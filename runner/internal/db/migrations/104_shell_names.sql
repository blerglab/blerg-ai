-- Presentation metadata only. Shell access still requires live broker authorization.
CREATE TABLE shell_names (
    shell_id text PRIMARY KEY,
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    owner_id text NOT NULL,
    login_id text NOT NULL,
    name text NOT NULL CHECK (char_length(name) <= 120)
);
