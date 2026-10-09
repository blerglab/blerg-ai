# MCP connections

An MCP connection lets an agent session use a remote [MCP](https://modelcontextprotocol.io) server
that you have added to your account: a tool set for your notes, a calendar, a ticket tracker, any
server that speaks MCP over HTTP. Blerg keeps the credential, and the session never sees it.

This page explains what a connection is and how a session reaches it. For unattended use see
[`crons.md`](crons.md); for the environment variables see [`core/docs/CONFIG.md`](../core/docs/CONFIG.md)
and the [runner configuration table](../runner/README.md#configuration-env).

## What a connection is

A connection is a name, a server URL and, usually, a credential. It belongs to one account and is
managed in **core Settings → MCP connections**.

- **Static token connections.** You give the URL, the name of the HTTP header the server expects
  (for example `Authorization`) and the secret value. Core stores the value encrypted in the same
  vault as your engine credentials, and never returns it again. Replacing it is a new value, not a
  reveal.
- **No-auth connections** for servers that need no credential.
- **Sign-in (OAuth) connections** for servers that support the MCP authorization flow. You give the
  URL and sign in at the server; see [OAuth connections](#oauth-connections).
- The name is a short lowercase slug, unique in your account. `board`, `blerg` and `gateway` are
  reserved. You can have up to 20 connections.
- **The URL must be `https`** and must not point at a private, loopback or link-local address. An
  operator can list exceptions (see [Network policy](#network-policy)).

## OAuth connections

Choose **Sign in** instead of a static token when adding a connection. Core discovers how the server
authorises (registering itself as a client where the server allows it; otherwise you supply a client
id), sends you to the server to approve, and stores what comes back.

- **Redirect URI.** The server redirects back to `<core public URL>/auth/mcp/callback`. Set
  `BLERG_CORE_PUBLIC_URL` to core's browser-facing address (the desktop stack does this for you),
  and register that exact URI if the server needs a pre-registered one.
- **Stored encrypted.** The refresh token and access token are kept in the same vault as your other
  credentials. The runner and sessions never receive the refresh token, only a short-lived access
  token when a call needs one.
- **needs_auth.** If the server stops accepting the refresh token (you revoked it there, it expired,
  the server changed), the connection shows **needs sign-in**. Tools from it stop working, and a
  cron that uses it records a failed run that says so. Use **Reconnect** to sign in again; your
  selected tools are kept.
- **Allow-lists apply to discovery.** The discovery, registration, token and revocation requests
  core makes follow the same [network policy](#network-policy) as the connection URL: `https` only,
  no internal addresses unless an operator lists the host.
- Removing the connection revokes the grant at the server where it supports revocation.

## The built-in board connection

Crons get a built-in connection named `board` (a reserved name) for the board the cron targets. You
do not add it or store a token for it. The runner's gateway obtains a short-lived token from core
that is valid for that one board and is revoked when the run ends. A cron sees a fixed set of board
tools: reading boards, columns and cards, and creating, updating, moving, commenting on and linking
cards. Archiving, column changes and board administration are not included. The operator points the
runner at the board with `BLERG_RUNNER_BOARD_URL` (see [`crons.md`](crons.md#the-target-board)).

## Tools are off by default

Adding a connection gives no session any access. When you start a session you choose, per
connection, which of its tools the session may call. In the runner's launch sheet each connection is
unchecked, and inside it every tool is off until you pick one of three presets:

- **Allow all** — every tool allowed; the sheet says how many of them can change things.
- **Require approval** — the tools the server marks read-only are allowed, every other tool is
  *proposed*: the call is queued on the Proposals page for your OK (see [`proposals.md`](proposals.md)).
- **None** — nothing attached.

**Customize** opens the per-tool list, where each tool is off, propose or allow on its own;
allowing a tool the server does not mark read-only shows a warning, because it can change
things. A selection that matches no preset shows as *Custom*. The crons form has no presets:
an unattended job's tools are chosen one by one.

Each choice is pinned to the tool's **definition hash**. If the server later changes a tool's
name, description or input schema, the pin no longer matches and the tool disappears from the
session until you choose it again. A server cannot quietly turn a harmless tool into a different
one.

You can save a per-connection default selection so the launch sheet starts from it. It is a
starting point only: it is verified against the server's live tool list every time a session
starts.

## How a session sees a connection: the gateway

A session never talks to your MCP server directly. The runner runs a small **gateway** on its own
listener (`BLERG_RUNNER_MCP_GW_ADDR`, separate from the runner's web port). For each connection you
enabled, the session is given one MCP server entry that points at the gateway, with a random token
that is valid for that session and that connection only.

The gateway:

- authenticates the token before reading anything else, and rejects a path that does not match the
  token's connection;
- answers only the MCP calls a tool client needs (`initialize`, `ping`, `tools/list`, `tools/call`
  and their notifications) and refuses everything else, including batches;
- lists only the tools you allowed whose pinned hash still matches, and refuses calls to any other
  tool;
- fetches your credential from core when needed and keeps it in memory for at most five minutes, so
  deleting or replacing a connection takes effect within minutes;
- returns text only, capped in size (256 KiB by default): images, audio and embedded resources are
  replaced with a short placeholder;
- limits a session to four concurrent calls per connection and a total call budget (200 by
  default), with a per-call timeout.

The session's config file, which holds the gateway tokens, is written with owner-only permissions
outside the working directory (inside the container for the sandbox, from a Kubernetes Secret for a
pod), so the tokens do not appear in a process listing, in `docker inspect` or in the Job spec. The
tokens are deleted when the session ends.

Only the runtimes that can contain a session carry connections: a **cluster pod** or the **Docker
sandbox**. A session on the bare host (This machine) is refused, because it has your full shell and
credentials. Connections also need the `claude` engine and a daemon or cluster that supports them;
the launch is refused with a specific message otherwise.

A session with connections is **private** to the account that started it. What else it may do
depends on who is watching ([design note](design/interactive-mcp-sessions.md)):

- A session **you launch from the launch sheet** keeps everything a session without connections
  has: shell, web, your settings, skills and always-on plugins. Only the MCP side is bounded, by
  the tools you allowed or set to propose. Its MCP servers are exactly the gateway's (no ambient
  server from the repository or a plugin), and the file tools are kept away from the gateway
  config directory. Such a session has a shell, so a hostile message could still make it send
  what its allowed tools return elsewhere; you are the control, as for any session.
- An **unattended** run — a cron, or a session the board starts with connections — runs with a
  restricted built-in tool set (file tools only, no shell or web access) and none of your settings
  or plugins. See [`crons.md`](crons.md#what-an-unattended-run-can-and-cannot-do).

## What gets logged

- Core writes an audit row each time the runner fetches a connection's credential (which
  connection, which session or token, when). If that row cannot be written, the credential is not
  returned.
- The gateway records every call (session, connection, tool, allowed or refused, outcome, duration)
  in `mcp_call_log`, and keeps it 90 days. Arguments and results are **not** logged.
- The credential itself is never logged or written to disk by the gateway.

## Network policy

Core and the runner both fetch the URLs you supply, so both apply the same rules to every
connection, at the moment the socket is opened and on the address actually dialled (so a name that
later resolves to an internal address is caught):

- `https` only. Redirects are not followed and proxy environment variables are ignored.
- Loopback, link-local, cloud metadata, private (RFC 1918 and IPv6 unique local), carrier-grade NAT
  and multicast addresses are refused, including their IPv4-mapped IPv6 forms.
- Response size and time are capped.

An operator can allow exceptions, for hosts they run themselves, with
`BLERG_CORE_MCP_ALLOW_HTTP_HOSTS`, `BLERG_CORE_MCP_ALLOW_PRIVATE_HOSTS`,
`BLERG_RUNNER_MCP_ALLOW_HTTP_HOSTS` and `BLERG_RUNNER_MCP_ALLOW_PRIVATE_HOSTS`. Each is a
comma-separated list of hostnames and is empty by default. Core and the runner read separate lists,
so set both.

## Security properties, and their limits

- A session holds a per-session, per-connection token, not your credential. Ending the session
  revokes it.
- The gateway is not reachable through the public ingress: on Kubernetes it is exposed only through
  an internal Service, and on the desktop stack only on the sandbox network.
- Another account's connection is indistinguishable from one that does not exist.
- **What a connection cannot protect against:** whatever an allowed tool returns is text an agent
  will read, so it is untrusted input. An agent that can call a tool that reads mail can be steered
  by the mail. Allow only the tools a job needs, prefer read-only ones, and treat anything an agent
  writes as a result of that input. For a Kubernetes install, also apply the default-deny egress
  policy for session pods (see
  [`install/k8s/CLUSTER-RUNTIME.md`](../install/k8s/CLUSTER-RUNTIME.md#crons-mcp-connections-and-the-gateway)).
