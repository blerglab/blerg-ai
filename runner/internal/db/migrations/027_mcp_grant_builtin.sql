-- Built-in connections (AI crons spec 10.2): the gateway serves a connection that core does not
-- hold, the board. Such a grant keeps the uniform shape, so the gateway code stays one path:
--   connection_id  a fixed sentinel uuid (never a core connection id)
--   builtin        which built-in it is ('board'); '' for a user's connection
--   builtin_ref    what the built-in needs (for 'board', the target board id)
-- url_snapshot holds the built-in's endpoint at grant time, like any other connection.
-- The hourly sweeper skips builtin grants: they are not in core's connection list.
ALTER TABLE session_mcp_grants
  ADD COLUMN builtin text NOT NULL DEFAULT '',
  ADD COLUMN builtin_ref text NOT NULL DEFAULT '';
