# Codex parity and security review

Status: shipped.

This review covers the credential and native recovery changes alongside the Codex
parity work already shipped. Independent agents reviewed code written by other
agents and attempted to break authorization, lifecycle and filesystem boundaries.
It is a source and integration review, not an external penetration-test certification.

## Supported behavior

Codex uses its persistent native app-server for mid-turn messages, model and effort
changes, capabilities, tools, compaction, background work and nested subagent activity.
Accepted browser input is durably queued. Session switching follows the bottom of
chat, and completed child reports cannot reopen a running spinner.

Host recovery retains native conversation IDs. New account-owned Claude and Codex
Cluster sessions additionally retain native conversation data across pod replacement.
Fresh credentials and grants are reconstructed independently of retained history;
earlier commands are not replayed. A missing expected native store refuses recovery.

Managed Codex login is a dedicated Cluster connection. Core owns refresh authority;
pods and catalog probes receive access only. Existing opaque imports remain snapshots.
The engine and credential adapter registries isolate provider-specific behavior.

Native Codex questions use the private ask/answer flow. Ordinary unrestricted turns
now enable the native question tool. Restricted sessions retain their explicitly
allowed messaging tools. The pinned CLI exposes native questions only to the root:
children send their question to the parent, which asks and forwards the answer.
Choices currently appear as text with a free-form answer, rather than native choice
buttons. Completion and interruption close stale cards.

## Findings addressed

| Boundary | Failure reproduced or checked | Correction |
| --- | --- | --- |
| Cluster credentials | Workers inherited the operator daemon key and could claim broader identity | Separate current-launch runtime and messaging pair; server-owned session, daemon and repository bindings |
| Runtime replacement | Validation could race replacement, leaving an old frame authorized | Shared distributed launch fence covers admitted frames, handshake, disconnect and bounded outbound writes |
| Terminal state | An admitted start could race Stop and revive its row | Scoped writes verify current token, unended session and nonterminal state at the actual database mutation |
| Identifier aliases | Alternate UUID spellings could select a different fence or legacy recovery path | Runtime locks canonicalize UUIDs; native recovery requires canonical session IDs before fallback |
| Host selection | A runtime pod could appear among workstation targets | Explicit and automatic host selectors exclude bound runtimes independently of reported metadata |
| Data plane | Pod reports could change the repository used by later authorization | Immutable launch repository/path and per-operation scoped checks; restricted sessions fail closed |
| Device login | Revoked browsers could commit a late provider result | Commit-time browser-chain and account verification with database locks; dead attempts and crashed leases can be replaced |
| Refresh | Concurrent or stale results could replace newer credentials | Durable claims and generation fences; import, delete and cancel invalidate late results; ambiguous rotation requires reconnecting |
| Disclosure | Revocation or audit failure during provider I/O could leave an access delivery | Authorization before and after provider I/O, generation-fenced audit, no disclosure on audit failure |
| Native child events | A foreign notification could self-grant child ownership | Bounded verified ancestry, persisted known-child allowlist and exact resumed root identity |
| Questions and RPC | Late questions could reopen after completion; blocked or duplicate replies could stall lifecycle processing | Current turn and child scope, completion seals, bounded writes and nonblocking duplicate response handling |
| Native storage | A deleted session could strand a claim, or a replaced directory could be trusted | Detached cleanup tombstones, immutable Kubernetes UID, owner/engine labels and pinned filesystem identity |
| Configuration export | Links could export private files under an allowed instruction filename | Anchored parent and inode checks, no symbolic or hard links, bounded actual reads |
| Configuration extraction | Linked destinations could overwrite unrelated files; oversized data could silently truncate configuration | Pinned root/parents, private temporary files and atomic replacement, per-file/aggregate/entry/decompression limits |
| Engine abstraction | A second registered managed adapter inherited Codex’s access mode | Adapter-owned access mode on cached and refreshed delivery; existing Codex JSON preserved |
| Board sockets | Restoring test keepalive defaults raced a socket goroutine | Synchronized defaults and one captured interval pair per connection |

The review also checked fresh grant enforcement after native resume, selected MCP
connections and budgets, removed-tool refusal, private callback authentication,
provider redirects and response limits, model-probe credential handling, launch
Secret contents, storage cleanup, native child cancellation, and safe UI errors.
No real credentials or native transcripts are used as committed test fixtures.

## Validation

Completed checks include the full frontend tests/types/production builds, full
contracts/Core and board database race suites, vet and installer/script checks.
Focused race tests cover managed login and refresh, runtime identity, model probes,
native recovery, child ownership/questions and configuration bundles. Real installed
CLI fixtures exercise both Claude and Codex native recovery; synthetic loopback
Codex fixtures verify external login, a 401-triggered refresh, retry, and absence of
persisted managed `auth.json`, including restricted sessions.

The runner database suite, repository-wide lint and vet, and the script, link and
scrub checks also pass, as do the credentials/API database race suites and a
second-engine and Codex wire-compatibility review of the adapter registry.

A live provider consent and paid inference are not automated by synthetic
authentication tests: the person must complete the dedicated login.

## Operational limits

- Existing sessions without native history cannot retroactively recover lost native
  storage. A retained session's workspace is reconstructed separately.
- Codex retains its native dynamic tool schema on resume. Removed grants are refused;
  advertising newly changed definitions may require a new native session.
- Unended native-backed sessions retain their claim; ended or detached history is
  reclaimed after the configured retention period. Host-path storage requests do
  not enforce a hard byte quota; operators need suitable storage and admission policy.
- Provider outages, revoked consent or ambiguous refresh rotation require reconnecting.
- Native identity is preserved separately from credentials and grants: a resumed
  session always uses the owner's current authorization.
