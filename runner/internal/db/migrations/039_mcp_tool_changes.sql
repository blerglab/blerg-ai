-- 039: MCP tool changes reviewed in place (docs/design/mcp-tool-changes.md §3).
--
-- A grant remembers which preset produced it (what auto_accept may grant for a tool the launch
-- never saw) and every tool name the launch listing contained (so a tool the person left off is
-- never mistaken for a new one). NULL known_tools means "unknown": grants from before this
-- migration, and launches whose listing was incomplete; such a grant never records a new tool.
ALTER TABLE session_mcp_grants
  ADD COLUMN preset      text NOT NULL DEFAULT 'custom' CHECK (preset IN ('allow_all','approval','custom')),
  ADD COLUMN known_tools jsonb;

-- Tool definitions by pin hash: the exact canonical bytes the hash was computed over, so a
-- review shows what was hashed, not a re-serialisation. Never truncated: a definition that
-- cannot be reviewed is stored with `unsafe` set and an empty canonical.
CREATE TABLE mcp_tool_definitions (
  hash       text PRIMARY KEY,
  name       text NOT NULL,
  canonical  text NOT NULL,
  size       integer NOT NULL,
  unsafe     text CHECK (unsafe IN ('hidden_chars','nul','too_large')),
  first_seen timestamptz NOT NULL DEFAULT now(),
  last_seen  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mcp_tool_definitions_last_seen ON mcp_tool_definitions (last_seen);

-- A tool change is something the person reviews: a pinned tool whose definition changed, a tool
-- the launch never saw, or a pinned tool the server no longer offers. One pending row per
-- (session, connection, tool). session_id is not a foreign key: a change outlives its session
-- until decided or expired, like a proposal.
CREATE TABLE mcp_tool_changes (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id      text NOT NULL,
  session_id      uuid NOT NULL,
  connection_id   uuid NOT NULL,
  connection_name text NOT NULL,
  tool            text NOT NULL,
  kind            text NOT NULL CHECK (kind IN ('changed','new','removed')),
  old_hash        text,
  new_hash        text,
  mode            text NOT NULL DEFAULT 'propose' CHECK (mode IN ('allow','propose')),
  muted           boolean NOT NULL DEFAULT false,
  state           text NOT NULL DEFAULT 'pending'
    CHECK (state IN ('pending','accepted','declined','superseded','expired')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  decided_at      timestamptz,
  decided_by      text
);
CREATE UNIQUE INDEX mcp_tool_changes_pending ON mcp_tool_changes (session_id, connection_id, tool) WHERE state = 'pending';
CREATE INDEX mcp_tool_changes_account ON mcp_tool_changes (account_id, state, created_at DESC);
CREATE INDEX mcp_tool_changes_session ON mcp_tool_changes (session_id, created_at DESC);
CREATE INDEX mcp_tool_changes_decided ON mcp_tool_changes (decided_at) WHERE decided_at IS NOT NULL;

-- Every pin event: a person or the auto_accept policy re-pinned or granted a tool, a person
-- declined, a row expired, a flapping server was throttled. Never holds definitions or arguments.
CREATE TABLE mcp_pin_events (
  id              bigserial PRIMARY KEY,
  account_id      text NOT NULL,
  session_id      uuid NOT NULL,
  connection_id   uuid NOT NULL,
  connection_name text NOT NULL,
  tool            text NOT NULL,
  action          text NOT NULL CHECK (action IN ('repinned','granted','declined','expired','throttled')),
  old_hash        text,
  new_hash        text,
  mode            text,
  actor           text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mcp_pin_events_session    ON mcp_pin_events (session_id, created_at DESC);
CREATE INDEX mcp_pin_events_connection ON mcp_pin_events (account_id, connection_id, created_at DESC);
CREATE INDEX mcp_pin_events_created_at ON mcp_pin_events (created_at);
