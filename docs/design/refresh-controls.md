# Refresh and retry controls

Read and discovery retries use the shared `IconButton` from `@blerglab/chat`.
The icon has a descriptive Refresh/Retry tooltip and accessible name, a disabled
spinning state while its promise or `busy` prop is pending, and a 44 px minimum
target on phones and coarse pointers. Callers keep responsibility for displaying
errors. Callbacks that start work should return its promise or pass `busy`.

## Audit (BLG-392)

Icon-only inline failures and refresh actions:

- Runner: workspace file refresh and Git status, repository status, branch history,
  new-channel options, launch boards, launch engine and model discovery, cron
  engines and boards, model catalog, proposals inbox and rail, board-card refresh
  with retained content, board filing, board-name refresh, plugin choices, MCP
  tools, workspace permissions, permission-modal child and board discovery,
  delegation rules, shell limits, workspace insights, work-data refresh, and
  session insights with retained figures.
- Chat: workspace-image retry, proposal read, and review conversation retry.
- Board: run-target, session-model, settings engine, and settings model discovery.
- Core: engine discovery and plugin discovery.

Labeled Retry controls retained:

- Runner board-card initial failure: no card content is available.
- Runner tool-change definitions: no definitions are available.
- Runner activity report initial failure: no report is available.
- Runner session insights initial failure: no figures are available.
- Board Git panel initial failure: no repository information is available.
- Board sign-in banner: the required action restores access to the app (or says
  Sign in after an expired sign-in).
- Runner permission save's “Retry failed changes”: repeats a write, so it keeps
  its explicit action label rather than presenting it as a data refresh.

Whole-view read failures keep visible labels; tool definitions, activity, session
insights and Git use `IconButton` with `labeled`. The board-card initial failure,
sign-in and permission writes retain their existing action controls.
