# Interactive sessions with MCP connections keep their tools

Status: design, 2026-10-05, amended after an adversarial review (see "Review amendments"). Applies to `runner` (server, daemon, pod runner) and the docs.

## The problem

Today a session is **restricted** the moment it carries an MCP connection: built-in tools cut to
file tools only (no shell, no web, no sub-agents), `--strict-mcp-config` (no ambient MCP server),
an empty `--setting-sources` (none of the person's settings, hooks, skills), no always-on plugins,
and the session is private to its account. That is the right containment for an **unattended**
run — a cron, or a session the board starts — which reads untrusted text with nobody watching.

It is the wrong shape for an **interactive** session the person launches from the launch sheet and
watches: attaching a connection to "stand up this app" takes away the shell the job needs, and the
plugins and settings the person relies on, while the thing that actually needed bounding — what the
MCP server may be asked to do — is already bounded by the per-tool allow / propose choices in the
picker. Nothing in the launch sheet says this happens; the plugins stage is simply absent.

## Decision

Restriction follows **who is watching**, not whether a grant exists.

| Start path | Grant | Restricted | Plugins, settings, shell |
|---|---|---|---|
| Launch sheet (`POST /api/sessions`, human principal), cluster or Docker sandbox | optional | **no** | as any session |
| Cron | its own | yes | none (unchanged) |
| Board-started session with connections (v1 start contract, `Grant`) | yes | yes | none (unchanged) |
| Resume | re-issued | **as it was started** | as it was started |

An interactive grant session keeps everything a session without a grant has, plus the gateway's
servers. What stays from the grant machinery, because it bounds the *connection* rather than the
*person*:

- The grant itself: only the tools the person chose, each pinned to its definition hash,
  `propose` tools frozen into proposals, the gateway's budgets and result caps.
- The session is private to the account (the credential audit assumes it, and it is harmless).
- A grant still needs a Claude agent-kind session in a cluster pod or the Docker sandbox, never
  the bare host, on a daemon that reports `mcp_gateway`.
- `--strict-mcp-config` stays: the gateway's servers are the session's only MCP servers. A pod's
  config bundle carries no user MCP servers anyway (CLAUDE.md, skills, agents, personas, plugins);
  what strictness drops is a cloned repository's `.mcp.json`, a plugin-bundled server and, in the
  Docker sandbox, the person's own servers — and with them any name collision with a gateway
  server. A person who wants an ambient server in a grant session adds it as a connection.
- The gateway config directory (the session's bearer tokens) is still denied to the file tools by
  the same `--disallowedTools` rules. In an unrestricted session this is hygiene against an
  accidental Read/Grep/Glob, not containment: the shell can read the file, and the paragraph
  above is why that is acceptable.

What a hijacked interactive session could do that a restricted one cannot:

- Reach its own gateway token through the shell and drive the gateway with `curl`. The token is
  valid for this session and this connection only, carries the same allow / propose list, pinned
  hashes and budgets the file tools get, and never reaches the upstream credential (the gateway
  fetches that from core per call; the gateway answers only `initialize`, `ping`, `tools/list`,
  `tools/call`). So on the MCP side the shell widens nothing.
- **Exfiltrate what its allowed tools return**, over the network, which a restricted session
  cannot. This is the real difference, and the control for it is the person watching the session,
  as for every other interactive session (which can already reach the network, the pod's
  environment and the shared daemon token). It is why unattended runs stay restricted.

## Changes

### Server (`runner/internal/server`)

- `HandlePostSessions` (cluster with repo, cluster no-repo, Docker daemon): `RestrictTools` is
  **false** regardless of the grant. `startRunnerSession` / `startBoardSessionOnDaemon` keep
  `req.Grant != nil || req.CronID != ""`.
- The decision is **recorded**: migration `036_session_restricted.sql` adds
  `sessions.restrict_tools boolean NOT NULL DEFAULT false`, back-filled `true` for every existing
  row with a `cron_id` or a grant (`session_mcp_grants`; grants are deleted only at a final end
  and a resume needs status `disconnected`, so the back-fill covers every resumable pre-change
  grant session). Every cluster start path writes it where the row exists (next to the posture
  and kind writes); `resumeClusterSession` reads it instead of forcing `true` when grants are
  re-issued. The Docker daemon has no resume of agent sessions, so its rows are not written.
- `pluginsWanted` drops `spec.MCPGateway == nil`; `ResolvePlugins` and `daemonPlugins` set a
  `PluginsNote` when they skip because the session is restricted ("not loaded into a restricted
  session"), so the start panel always says why there is no plugins stage.
- `k8sjobs` keeps withholding the codex and hermes secrets from any pod with a grant (a grant
  session is Claude-only, so they are of no use to it).
- `mcpstart.go`'s header comment is rewritten to the table above; `mcpstart_parity_test.go` keeps
  enumerating the five spawn paths and now asserts each path's `RestrictTools` value.

### Daemon (`runner/internal/daemon`)

`ccOptions.MCPConfigPath` no longer implies restriction. The turn's arguments become:

| | `--mcp-config` | `--strict-mcp-config` | `--tools` allow-list, empty `--setting-sources`, `--disable-slash-commands` | `--disallowedTools` | plugins, session guide |
|---|---|---|---|---|---|
| no grant, not restricted | – | – | – | – | yes |
| grant, not restricted (new) | yes | yes | – | config-dir and runner temp-dir rules only | yes |
| restricted (grant or not) | yes (or the empty config) | yes | yes | the full `ccPathDenyRules` | no |

`ccPathDenyRules` is split into `ccGrantDenyRules(configDir)` (the gateway config directory and
the generic runner temp pattern) and the rest; a restricted turn gets both. The session guide
(`--append-system-prompt`) is appended for every unrestricted turn, grant or not: the three sites
that key it on "no grant" today (`ccCommonArgs`, `runTurn`, the stream driver) key on
`RestrictTools`.

`mcpGatewaySpawnProblem` is unchanged (agent kind, sandbox). `AgentHost.spawn` drops plugin dirs
only for `RestrictTools`, not for a grant; `ClaudeOnly` stays `RestrictTools || grant` (a grant
session is Claude-only, mirroring the pod withholding codex and hermes secrets).

**Older daemons.** A daemon built before this change restricts on the grant alone, whatever
`restrict_tools` says, so a launch-sheet grant session on it is still restricted (and gets no
plugins). The desktop daemon ships with the runner; update it. The server does not refuse such a
daemon: the session is safe, only narrower.

### Pod runner (`runner/internal/runner`)

`skipsUserConfig()` and `podSpawn`'s `RestrictTools` follow `cfg.RestrictTools` alone; a grant no
longer implies either. The config bundle is downloaded and plugins are installed for an
unrestricted grant session.

### Docs

`docs/mcp-connections.md` (the paragraph that says sessions with connections run restricted),
`SECURITY.md` ("A cron or a session with MCP connections" → "an unattended run with MCP
connections"; a new line on what an interactive grant session can reach — the gateway token and
exfiltration, with the pod's own environment and the shared daemon token named as already
reachable by any interactive cluster session), `docs/crons.md` (unchanged in substance; one
cross-reference), `runner/README.md` where `RestrictTools` is described, `CHANGELOG.md` (Changed:
behaviour change for launch-sheet sessions with connections). The launch sheet says next to the
connections that a session with connections is private to you.

## Verification

- Unit: the five spawn paths' `RestrictTools`; resume reads the stored flag (a cron row stays
  restricted, a launch-sheet row with grants comes back unrestricted, a pre-migration grant row is
  back-filled restricted); `pluginsWanted` with a grant; the plugins note on a restricted start;
  daemon argument builder for the three rows of the table above (including that the config-dir
  deny rules are present without the allow-list); pod `skipsUserConfig` and `podSpawn`.
- On a staging cluster, after deploy: a launch-sheet cluster session with a real connection on
  **Require approval** shows the plugins stage, has a shell (`make check` runs), lists its MCP
  tools and its plugins' skills. A `Read` of the gateway config directory being refused is checked
  too, as hygiene; it is not what the design rests on.

## Review amendments (adversarial review, 2026-10-05)

1. The security argument now states the real delta — exfiltration over the network — and names
   the person watching as the control, instead of "the shell adds nothing".
2. `--strict-mcp-config` is kept for grant sessions: the pod bundle has no user MCP servers, and
   a user or project server named like a connection would collide with the gateway entry.
3. The config-dir deny rules are hygiene in an unrestricted session, not containment; the
   staging check is kept but demoted.
4. Grants table is `session_mcp_grants`; the flag is written where the row exists; the Docker
   daemon has no agent-session resume.
5. The session guide's three sites, `ClaudeOnly`, and older daemons are spelled out.
6. Tests to update are named: `restricttools_test.go`, `mcpstart_parity_test.go`,
   `k8sjobs_restricted_test.go`, `daemon_plugins_test.go`, daemon `mcpgateway_test.go`, pod
   `mcpgateway_test.go`.

## Not changing

- Unattended runs: crons and board-started grant sessions are contained exactly as before.
- The gateway, grants, proposals, pinning, budgets, privacy of grant sessions.
- `This machine` (bare host) still cannot carry a grant.
