-- Categories are shared by a workspace's channels, not browser preferences.
CREATE TABLE workspace_channel_categories (
 session_id uuid PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
 state jsonb NOT NULL DEFAULT '{"categories":[]}'
);
