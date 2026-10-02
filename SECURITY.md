# Security policy

Blerg runs coding agents that execute code, hold engine and git credentials, and can
reach your repositories. Please treat anything that weakens those boundaries as a
security issue, not an ordinary bug.

## Reporting a vulnerability

**Do not open a public issue.** Use GitHub's private reporting instead: on this
repository, open **Security → Report a vulnerability**. Only the maintainers can see
the report.

Please include:

- what is affected (core, board, runner, the daemon, an installer) and the commit or
  version you tested;
- how to reproduce it, as concretely as you can;
- what an attacker gains.

Never include a real credential, token or private key in a report. If the issue is
that one is exposed, say where, not what it is.

You can expect an acknowledgement within a week. Blerg is maintained on a best-effort
basis, so fixes are prioritised by severity; we will keep you informed and credit you
in the fix unless you ask us not to.

## Supported versions

Blerg is pre-1.0. Only the current `main` receives security fixes.

## What is in scope

- Bypassing authentication or a capability check in core, the board or the runner.
- Reading or using another account's stored credential.
- A session escaping its sandbox container or cluster pod.
- A credential, token or secret reaching a log, an API response, an event or a file it
  should not.
- A session, its events or its output being visible to an account other than the one that
  started it, when that session is private (see below).
- A session with MCP connections or an unattended cron run reaching a connection, a tool or a
  credential that was not selected for it, or the gateway being reachable outside the session
  network.
- Getting the runner's gateway or a connection URL to fetch an internal address (a bypass of
  the network policy in [`docs/mcp-connections.md`](docs/mcp-connections.md#network-policy)).
- An installer default that exposes a service or a secret.

## What is not a vulnerability

These are documented properties of the design. They are worth knowing before you
deploy, but they are not bugs:

- A session launched on **This machine, unsandboxed** runs as the daemon's user with
  that user's full access. It is never a default, and the launch sheet requires an
  acknowledgement before it will start one (a caller of the runner's API with a valid token is
  not asked).
- Every account on one install can launch sessions on any connected daemon. One core
  per developer machine is the supported desktop shape.
- Anyone who can write cards on a board can spend the credential behind that board's
  automation token, and could get an agent to reveal it.
- The Local sandbox mounts your engine logins so the engine can use them; it does not
  protect those credentials from the code the agent runs. A sandbox cron on a desktop
  therefore runs on the host developer's Claude login and on any connected daemon.
- **A hostile message can still steer an unattended agent.** A cron or a session with MCP
  connections reads whatever its allowed tools return (mail, tickets, web pages), and that text
  can carry instructions, including a forged card or a proposal worded to look routine. The controls are containment, not detection: no shell or web tools,
  file tools only, only the connections and tools you selected, each pinned to its definition,
  capped results and call budgets, private sessions and no fallback to a shared credential. What
  a hostile message can still do is misuse the tools you allowed: read other data reachable
  through an allowed tool, or write misleading content (a card, a file, an outgoing request) with
  a tool that can write, or queue a proposal a hurried approver might accept. Read the frozen
  arguments before approving. Give a run only the tools its job needs, and prefer read-only ones.
- **Known limits of the containment.** The agent's file tools run as the same user as the runner.
  A restricted session adds path deny rules (`/proc`, `/etc`, the gateway config directory, the
  engine login under `~/.claude`, common credential dotfiles) that Claude Code enforces even with
  its permission prompts off (proven on the pinned version); an engine upgrade must re-prove them.
  A cluster session pod still holds the shared daemon token in its environment (the server has no
  narrower pod credential yet); the rules above keep the file tools away from it, but a bug in the
  engine's permission checks would not. Restricted pods carry no codex or hermes secrets.
- **The repository the agent works in.** A restricted session's file tools are denied the checkout's
  `.git` folder (it holds the clone credential in a cluster pod and settings that git executes), and
  may not write the agent-tool config files that the next unrestricted run in that checkout would
  read (`.claude/`, `.mcp.json`, `.vscode/`, `.envrc`, `.husky/`, `.githooks/`). Everything else in
  the project stays writable, and a Local sandbox bind-mounts your real checkout read-write, so a
  hijacked unattended session can still change ordinary files there. Point crons at a scratch clone,
  not at a checkout you work in. The runner's own git commands ignore a repository's hook,
  fsmonitor and credential-helper settings.
- **Crons and board runs.** A cron that writes cards writes into the board that board runs (the
  dispatcher) pick work from. If you enable runs on a board that a cron also writes to, a card a
  hijacked cron writes into the ready column can be picked up by an unrestricted worker session.
  Keep cron boards separate from boards with runs enabled, or leave runs off where crons write.
- **The shared daemon token on file routes.** A session pod or daemon holds the shared daemon token,
  and the runner accepts it on the session file routes (publish, upload, fetch) without checking the
  session binding or a private session's owner. Anything that holds it and knows a session id can
  read that session's attached files and add published files to it. Treat the token as able to do
  that, and keep it off any machine you would not trust with every session's files.
- **Storage.** A session may publish up to 50 files of 25 MiB (about 1.25 GiB), and on Kubernetes the
  runner's data is an `emptyDir` unless you give it a volume. A runaway agent can fill node
  ephemeral storage: set ephemeral-storage limits or a quota for the namespace.
- **HTML artifacts.** A published HTML page runs in a sandboxed frame with no network, no form
  submission and no embedding, but it can still draw a convincing page inside the viewer. Treat a
  file's content as untrusted, as its author is an agent that may have read hostile text.
- **Gateway tokens** are valid until the session ends rather than for a fixed time; core checks that
  the session is still live on every credential fetch (a live answer is cached for up to five
  minutes).
- The gateway proxies your MCP server's responses as text. It cannot vouch for what a server
  says, and a compromised server can attack the agent through its tool results.
- Membership of the `docker` group is equivalent to root on that machine.

## Running Blerg safely

- Keep the desktop stack bound to `127.0.0.1` (the default) unless you have put
  authentication and TLS in front of it.
- Core's `/internal/*` routes are for the components inside your cluster or compose network, which
  call core directly. Core refuses any request that carries a forwarding header (one that came
  through a proxy or ingress) even with the right key, but do not add routes for `/internal` to a
  proxy of your own.
- Back up `BLERG_CORE_LOCAL_KEY`. Losing it loses every stored credential; leaking it
  exposes them.
- Prefer the Local sandbox or the cluster runtime over unsandboxed sessions.
- Only point a session at code you would be willing to run yourself.
- Cluster session pods are hardened by default: no service-account token, non-root,
  no privilege escalation, all capabilities dropped, the runtime's default seccomp
  profile. They can still reach in-cluster services (Postgres, core, the board) over the pod
  network unless you apply the optional
  [`install/k8s/networkpolicy-sessions.example.yaml`](install/k8s/networkpolicy-sessions.example.yaml),
  which blocks core and Postgres, leaves the board off unless you opt in, and needs a CNI that
  enforces NetworkPolicy; see
  [`install/k8s/CLUSTER-RUNTIME.md`](install/k8s/CLUSTER-RUNTIME.md).
- Always-on plugins run third-party code in every cluster session you start. Only a signed-in
  human can change the list (an agent token cannot), and only marketplaces the operator allows
  (`BLERG_CORE_PLUGIN_MARKETPLACES`, default: the official Anthropic marketplace) can be used;
  add plugins you trust, and widen the allow-list deliberately. A plugin can act with the
  session's full access once installed.
- **MCP connections and crons** ([`docs/mcp-connections.md`](docs/mcp-connections.md),
  [`docs/crons.md`](docs/crons.md)). A connection is a standing credential to a third-party
  server: only a signed-in human can add one, the stored value is encrypted like your other
  credentials and is never returned, and every fetch of it is audited. A session never holds it: it
  holds a per-session, per-connection gateway token that is revoked when the session ends. The
  gateway runs on its own listener that the public ingress does not route; on Kubernetes apply the
  default-deny egress example so session pods can reach only DNS, the gateway and the in-cluster
  services they need. Sessions with connections and every cron session are **private** to the
  account that started them: other accounts, administrators included, cannot list, read or receive
  events from them. Connection URLs are `https` only and refused if they resolve to an internal
  address, unless an operator lists the host in the `BLERG_*_MCP_ALLOW_*_HOSTS` variables; list
  only hosts you run.
- **Proposals** ([`docs/proposals.md`](docs/proposals.md)). A write tool you set to "propose" is never
  forwarded to the server when the agent calls it: the call is frozen and queued, and only a
  signed-in human, the owner of the session, can approve it. What runs is exactly the frozen
  arguments, which the page shows; the agent's own summary is displayed separately and labelled as
  the agent's. An approval is refused if the connection's URL or the tool's definition changed since
  the call. Pending proposals expire after seven days, and an approval whose outcome is unclear is
  never retried automatically.
- **Board access for crons.** A cron's built-in `board` connection does not store a board token. The
  runner's gateway asks core for a short-lived token (10 minutes) that is valid for one board only,
  carries a fixed small set of card and column capabilities, never includes archive or board
  administration, and is revoked when the session ends. Cards an agent writes still pass the board's
  admission gate.
- **OAuth connections.** The refresh token and access token core obtains when you sign in to an MCP
  server are stored encrypted, like your other credentials, and are never sent to the runner, a
  session or the browser; the runner receives only a short-lived access token when it needs one.
  The sign-in flow uses PKCE, a single-use state bound to your login, and an exact redirect-URI
  check. Removing the connection revokes the grant at the server where it supports that.
