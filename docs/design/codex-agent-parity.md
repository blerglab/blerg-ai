# Codex agent integration parity

Status: runtime integration implemented, with the remaining limits below. Interactive runtime, durable messaging, managed native plugins,
restricted file/MCP tools, scheduled runs and terminal recovery have separate protocol,
regression and adversarial checks. Updated workstation daemons and session images are required.

## Runtime

Use the [Codex app-server protocol](https://developers.openai.com/codex/app-server) rather than one
`codex exec` process per message. Its JSON-RPC interface supplies thread start/resume, turn start/steer,
interrupt, effective model/effort, runtime inventory and structured item/subagent notifications.
The session images pin the tested CLI version. Generated schemas are inspected during development;
only display-safe whitelisted fields are mapped into Blerg events. Credentials and MCP process stderr
are never forwarded as errors or inventory metadata.

One process serves a session, with independent RPC response reading so a notification backlog cannot
block the response needed to drain it. The driver retains the thread identity across bounded process
reconnection. Ambiguous transport failure during input submission stops that process; it does not
blindly submit the message a second time. Real input takes precedence over queued recovery continuation.

## Input acceptance

The server commits an `input_accepted` transcript event and outbox entry in one transaction before
forwarding a correlated browser input. Queued bubbles live outside the mounted chat component and can
also be reconstructed from pending acceptance events after reload. Consumption or an explicit
client-correlated error settles the outbox before the event is acknowledged. Disconnected cluster
resume carries the same client ID through the Job's initial prompt.

An updated daemon advertises `input_receipts`. Only daemons with that capability receive reconnect or
heartbeat redelivery: older adapters would otherwise execute duplicates. Private receipt journals
retain pending IDs and consumed events, replay the original event UUID without applying turn
bookkeeping again, and evict completed receipts only after the server acknowledges persistence.
A receipt-storage fault cancels and ends the session without waiting for a potentially full event
buffer. Session endings fail remaining inputs; reopening repairs a transient terminal-settlement error.

The bounded queue accepts 64 pending messages and 4 MiB total, with a 256 KiB serialized-message limit.
Reuse of a client ID with different content is refused. Automatic ask routing and input redelivery
share a session lock, so one message cannot become both a prompt and an answer to an open question.
This does not claim an atomic transaction across the engine's own persistence and Blerg's database.

## Recovery and safety

Host Codex sessions retain their thread ID, effective configuration, current turn and open work.
Recovery checks for the retained transcript and uses the same guarded continuation plan as Claude:
questions and destructive commands wait for a person, continuation is attempted once, and a person's
message wins. Missing transcripts are explicitly reported. No initial task is silently replayed.
Sandbox/pod process replacement and managed grants have their own lifecycle constraints.

## Adversarial review and evidence

Independent review found and drove regressions for: startup interrupt races, notification/RPC
backpressure, provider-limit resumption, stale model attribution, missing transcripts, lowercase shell
command safety checks, consumption of recovery continuation after a user message, outbox bounds and
conflicting IDs, old-daemon replay compatibility, terminal settlement rollback, ask-answer races,
unacknowledged receipt eviction, failed receipt rollback and receipt replay side effects.

A real installed-CLI smoke test in a disposable workspace verifies effective settings, capabilities,
a short answer, mid-turn steering during a harmless sleep, interruption, same-thread resume, subagent
start/end and child transcript routing. It is opt-in (`BLERG_REAL_CODEX=1`), does not print raw engine
errors, and is complemented by fake protocol, race, browser and database-backed tests.

Native questions use private session asks with correlated answers and turn cancellation. Secret
questions are refused rather than placing credentials into chat. A replacement pod without native
history passes a bounded archive of roles, messages and tool outcomes to its next thread, explicitly
labelled as untrusted transcript data. The displayed user message stays unchanged, and the session
reports that this is a reconstructed context rather than claiming a native conversation resumed.

## Managed and restricted runtimes

Managed plugins use immutable per-session native Codex snapshots, frozen local marketplace
catalogues and private launch configuration. The installer has no account credentials. Selected
plugins do not mutate the person's configuration or workshop cache, and their conversation paths
remain stable across native resume and host/container launches. Daemons advertise supported plugin
engines; the legacy plugin flag means Claude only.

Restricted/cron sessions use an auth-only private Codex home, an exact verified CLI version and
explicit dynamic tools. Native shell, network, delegation and ambient configuration are disabled.
Real model-request capture verifies that a malicious project configuration cannot re-enable tools.
Newer models retain confined JavaScript orchestration, clock and empty native-resource lookups;
their tool inventory is checked separately. Native patch writes are denied by the read-only
sandbox, while permitted file changes go through the rooted dynamic tools. A real model protocol
test verifies that a native patch cannot write protected configuration.
File operations use rooted, no-follow handles and reject symlink/hardlink aliases and executable
control state. Gateway tokens remain in the daemon-side proxy, which refuses redirects and reports
safe errors. Restricted Codex requires Linux; older daemons do not advertise or receive this runtime.
Crons persist their engine and select compatible runtimes with the same owner/grant rules.
Interactive grants preserve native shell, skills and selected plugins. Their copied configuration
and private plugin snapshots remove ambient MCP entries, and explicitly disabled merged system
servers close the native configuration map-merge loophole. Host and Docker tests verify those
servers do not start while the selected skills and explicit gateway tools remain available.

Codex terminals have separate persistent native history homes, with fixed links to their existing
login and settings. Recovery selects that terminal's latest valid interactive thread, excluding
child/exec histories, and resumes its exact identity. Missing history is explicitly refused without
replaying the kickoff prompt. A real terminal/PTY test against a local fixture provider verifies
same-thread resume after process loss.

Host agent recovery remains limited to eligible unrestricted sessions, as for Claude. Restricted
sessions never regain a grant merely because a daemon restarted; cluster replacement uses the
server's existing authorization and grant lifecycle. Durable receipts reduce duplicate delivery but
cannot make engine-side execution and server-side persistence one atomic transaction.

## Follow-up review and remaining limits

A second independent review added regressions for completion-versus-steering races, ambiguous
accepted responses, input starvation behind streamed output, repeated child follow-ups, per-run
usage baselines, cached-token accounting, native model/effort constraints, MCP inventory failures
and startup/auth notifications. Session shutdown now waits for driver cleanup before deleting
its runtime resources, with a bound for an unresponsive driver. Plugin snapshots and terminal
history use scoped filesystem handles that reject escaping links.

Cluster model catalogs persist by account, engine and image with nonblocking background refresh.
Chat, launch and cron share runtime targeting, bounded requests and retry. Public catalogs remain
available; Cluster never substitutes a workstation's native catalog. Unknown engines are rejected
by shared launch and recovery paths before driver selection.

Durable Cluster OAuth refresh is provided by managed connections; see the
[engine credential lifecycle](engine-credential-lifecycle.md). An imported snapshot is not a managed
connection. Replacement pods without native history reconstruct bounded context as described above.
Direct native interactive questions from a child thread are not yet routed separately; questions
from the root thread use the supported ask/answer path. After a restart, a child must be
rediscovered from native thread/delegation metadata before otherwise unscoped child notifications
can be attributed safely. These limits must not be presented as full
native behavior parity across every runtime.
