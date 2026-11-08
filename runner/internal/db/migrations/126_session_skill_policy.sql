-- Keep the applied inventory stable across process and pod recovery.
CREATE TABLE session_skill_policy (
    session_id uuid PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    policy jsonb NOT NULL
);
