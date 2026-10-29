# Owner-shell channel

The owner-shell implementation is separate from agent terminal input, output,
History, tmux snapshots, and session resize. It never uses admin viewing
permission as permission to execute as another person.

## Core policy

Migration `core/internal/db/migrations/027_shell_policy.sql` adds the install-wide
switch, initially enabled, with a durable monotonically increasing revision.
Sandbox and pod shells are eligible when enabled. Host shells additionally
require fresh consent for the exact launch and authenticated machine/account
identity; the switch alone never enables host execution.

An administrator uses **Settings → Owner shells**, backed by
`GET/PUT /api/settings/shell-policy`. PUT accepts only `{"enabled":true|false}`,
requires a same-origin request, and checks the exact live login and current
persisted administrator role. Changing the switch increments the revision even
when the value is unchanged. There is no per-person exception to the switch.

Runner uses the authenticated control plane (`X-Internal-Key`):

- `POST /internal/shell/authorize` accepts `owner_id` and `login_id`. It verifies
  that exact human login, without fetching credentials, and reads current policy.
- `POST /internal/shell/control` streams NDJSON policy and revocation snapshots.
  PostgreSQL commit-time notifications wake readers on policy, login, account,
  and identity revocation changes, including changes from another core replica.
  One-second snapshots also monitor delivery. Stalled, interrupted, malformed,
  unknown-version, or unavailable authority fences the runner channel.

A runner restart starts without policy authority. Neither cached policy nor old
handles are restored. A revision change revokes existing handles; re-enabling
requires opening new shells. Disconnected runtimes cannot learn a new policy
instantly: grants last at most 30 seconds, renewed every 10 seconds.

## Runner transport and grants

`GET /api/sessions/{id}/shells` lists only the exact live login’s handles for
restoring tabs and explicit takeover; there is no cross-login or admin listing.
`POST /api/sessions/{id}/shells` has an empty body. It returns opaque handle and
shell IDs, runtime, and working-folder display metadata. Command, environment,
and cwd input from the browser are refused. `DELETE /api/shells/{handle}` requests
termination. Both require a verified human login and a valid Origin.

`GET /api/shells/{handle}/socket` upgrades a dedicated WebSocket using the
`bearer` subprotocol plus the access token, echoing only `bearer`. No credentials
appear in URLs. The socket accepts input (base64 bytes), resize, and heartbeat
frames; output is binary. Input and resize sequences are independent and scoped
to an attachment. Each shell has one controller. An explicit `takeover=true`
upgrade revokes the previous attachment. Reconnect requires fresh authentication
and a fresh signed grant; there is no server output replay archive.

Every operation and output delivery rechecks the persisted owner, session state,
manifest generation, runtime connection, consent binding, live human login, and
policy revision. Children, cron sessions, unowned legacy sessions, ended or
archived sessions, discard requests, paused/stopping/replacing runtimes, old
capabilities, agent tokens, and other people (including administrators) fail
closed. Running, waiting, and idle agent states allow shells only while the
runtime adapter also reports live admission.

`ownershell.SignedGrant` authenticates the unchanged P0 grant with an ephemeral
Ed25519 runner key. The grant audience includes the authenticated connection and physical runtime
generation via `ownershell.RuntimeAudience`; selection launch generation alone
cannot fence a replacement on the same daemon connection. Trusted bindings also
carry the prepared root identity and manifest digest. The runtime must pin that public key over its existing
outbound authenticated connection, along with the expected audience, owner,
login, session, launch, connection, shell generation, runtime, consent, and policy
revision. `VerifyGrant` rejects mismatches, tampering, future issuance, expiry,
and leases over 30 seconds. `RuntimeLease` supplies local per-operation checks,
renewal, policy revocation, independent expiry ticks, and retained teardown errors.
It never resurrects an expired or revoked shell.

Four shells share a session limit of 16 KiB/s input and 64 KiB/s output. Frames
contain at most 16 KiB of decoded bytes. Each controlling attachment has a bounded
output queue of at most 256 KiB (16 frames); slow consumers close. The browser
also bounds pending terminal parsing and input queues. Attachment heartbeat is
15 seconds with a 45-second lease.

Two inactivity limits end a shell: one with no browser attached (`detached`), and
one with no human input (`idle`); output, resize, and heartbeat do not reset the
idle limit. Each is set per runtime class, install-wide, by an administrator in
**Settings → Shells** (`GET`/`PUT /api/shells/settings`, `account.manage`): the
runtime default (`null`), "until the session ends" (`0`), or a duration from 10 minutes
to 87,600 hours. Desktop
shells (host and local sandbox) default to "until the session ends" for both;
cluster shells default to a five-minute reconnect grace and 30 minutes idle. A
change applies to running shells. When one limit gets shorter, a running shell keeps
at least 10 minutes from that change before it can end the shell, but never longer
than the deadline it already had; changing the other limit, or lengthening one,
never moves a deadline. When a limit ends a shell, its tab says which
one (the socket's `ended` frame, readable only by the same login for 24 hours) and
keeps its last screen; a sign-out or sign-in change erases it like any other shell.
Every other end is unchanged: the session ending, revocation, a fence, a policy
change, or a lost runtime connection (a desktop that sleeps or drops its network
still ends its shells).

Audit records have only actor/login IDs, session/runtime/connection identity,
launch/shell generation, policy revision, event, fixed reason, timestamp, and
duration. Shell bytes, commands, environment, cwd, credentials, and transport
errors have no audit fields and do not enter agent broadcast or persistence paths.

## UI

`ShellView` renders xterm through `ShellTransport`; it does not import the agent
WebSocket. Scrollback is local and bounded to 2,000 lines. Clipboard OSC commands
are consumed locally rather than permitted to write the browser clipboard.

The Shell button opens a permanent session tab, labeled Shell, Shell 2, and so on.
Its tooltip identifies host, sandbox or pod and the working folder. The session
supports four shells; the button and New shell command explain when the limit is
reached. Ctrl/Command+backtick focuses an existing shell, or opens one when none
exists. Inside terminal input, only Ctrl+K/P (Windows/Linux) or Command+K/P (macOS)
open the palette/quick open. Windows/Linux therefore reserve Ctrl+P from shell
history and Ctrl+K from kill-line. On macOS all Control combinations reach the
shell. Tab shortcuts operate outside terminal input; typing never closes a shell
or switches tabs.

Shell tabs reorder and move into the standard tab group's split overlay, just like
files. Split dividers fit terminals to their new geometry, including a trailing
server resize after dragging. The terminal uses Blerg's code font and theme tokens;
open terminals follow explicit and system light/dark theme changes.

Closing a shell tab requests termination and immediately removes it, including when
it is disconnected. If termination cannot reach the server, the existing lease and
reaper clean it up. Switching tabs keeps the terminal mounted and consuming output.
Reload restores live shells into their saved tab positions and split groups. Failed capability or inventory refreshes preserve tabs and connections. Closed
shell IDs are remembered locally until a successful inventory no longer lists
them; a close before inventory arrives queues termination until its handle is known.
Only shell IDs and tab metadata are saved locally, never handles or terminal bytes.
Phones use full-height tabs, the same open-tab switcher as files, and Back to Chat;
they no longer need a separate modal shell sheet.

## Runtime integration gate

The concrete supervisors, consent flow, shared scheduler and P0b adapters are
implemented. See [Owner-shell supervisors](owner-shell-supervisors.md) for their
registration and lifecycle interfaces, platform limits and security gates.
Shell and workspace admission share per-runtime policy: Linux/WSL host and local
sandbox are ON when their installed backend verifies. Cluster workspace and shell
policy is ON after the real-pod happy-path smoke; macOS host remains OFF (#181). Exact
negotiation, owner policy and host consent remain required. No environment variable
enables an OFF runtime. See [Workspace integration](workspace-integration.md).

## Review repairs

Policy mutation locks the policy row, then the existing account-session advisory
fence, account row and exact login rows; authorization is re-read after lock waits
and retained through commit. No account/session lock is held while queued on the
policy row. Direct account updates therefore serialize as well as fenced logout.

Manager database/core/runtime/browser calls run outside the global map lock with
per-operation deadlines. A shell permits one runtime/write operation in flight;
closure cancels it. Disable/revocation removes all affected handles before bounded
parallel cleanup. Uncertain cleanup retains admission fencing. Tick gives each
shell its own context and uses up to eight workers. Shared per-session frame
quotas bound input/output/control work before authorization calls. The daemon/pod shared writer now schedules shell control, shell data and ordinary
agent/file lanes with bounded waits and write deadlines.

Automatic terminal replies carry no human activity. Keyboard/paste/IME events
use a separate authenticated, rate-limited activity frame (coalesced once a second
in the browser). Local screen and bounded scrollback survive tab changes and split moves because
the terminal instance stays mounted. Explicitly opening or selecting a shell focuses its input on desktop only.
Restore never focuses terminal input, and phone tab switches never open the keyboard.
