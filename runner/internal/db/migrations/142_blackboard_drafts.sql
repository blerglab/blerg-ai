-- A blackboard draft is a proposal for a workspace's blackboard setup (vision, goals, first
-- milestone, thrusts with reasons) generated server-side from the board, release history and
-- commit log. One pending draft per workspace; discarded or replaced wholesale, never patched.
-- 16KB matches the bound used for other per-row jsonb proposal payloads (mcp_proposals.arguments
-- is capped at 64KB, answer_requests.schema/result at 8-16KB): a draft holds only a handful of
-- short fields, so 16KB is generous while still bounding worst-case untrusted text (board card
-- titles, commit subjects, release names) carried inside it.
CREATE TABLE blackboard_drafts (
 workspace_id text PRIMARY KEY,
 draft jsonb NOT NULL CHECK (pg_column_size(draft) <= 16384),
 created_at timestamptz NOT NULL DEFAULT now(),
 created_by text NOT NULL
);
