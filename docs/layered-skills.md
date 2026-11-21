# Layered workflow skills

The shared session guide contains safety, authorization and session-operation
rules plus a small skill catalog. Full ticket, coordination, testing and UX
instructions are installed outside the repository in a private session pack.
Content hashes identify exact versions. Full skill text has no always-on size
budget; the catalog does.

Claude receives the pack as a native plugin directory, including on its default
streaming launch and after plugin updates. Its catalog uses only `blerg:<name>`;
bare repository skill names are never attributed to Blerg. Codex and Hermes
receive exact private paths in developer instructions and use file readers.
Codex CLI 0.162.1 does not discover these files through `skills.config`; no such
configuration is sent. An explicit `/blerg:<name>` input instead verifies the
private file and sends its text through the acknowledged turn input. The built-in
loop exposes the same files through its namespaced Skill tool. The catalog tells
native subagents to use the same private paths; workers and channels receive
their own policy-resolved packs. Invisible native child reads remain a reporting
gap, not a confirmed load.
OpenClaw's stateless `agent exec` adapter and restricted sessions receive compact
rules because their adapters cannot promise native skill discovery. OpenClaw
also receives the session-operation guide and marker instructions, included in
its replay budget; Hermes receives marker instructions too. Mandatory guidance
and the current message take priority over old replay turns. The compact fallback
never expands to the full skill text.

Install, account and workspace off switches still intersect. Disabled skills are
absent from both the prompt catalog and the session pack. Only Main receives
coordination. Shared workstation discovery links are not used to distribute
session-specific policy. Packs survive driver replacement; terminal session
cleanup removes them after their writers stop.

`TestSkillContextByteCost` reports initial guidance and cumulative source bytes,
including on-demand loads. In the first review, representative Main values were 32,219 bytes before,
12,302 initially with private paths, 15,887 with one UX load, 19,472 with two UX
loads, and 29,515 when all four skills load once. Coordination was trimmed from
8,947 to 5,081 bytes and testing from 5,354 to 4,422 while retaining their rules.
Thus loading every skill is only about 8.4% below the old Main prompt, not a 64%
whole-session saving.
Actual repeated reads add their cost again. These are UTF-8 source/guide bytes,
not tokens or the engine's rendered total prompt. Engine-owned instructions,
workspace content, marker instructions and message wrappers are outside this
comparison. Generated private path lengths cause differences between runs.

## Skill telemetry and Insights

`skill_load` is an ordinary durable agent event, beside `turn_work`. It records
session, native subagent scope, canonical role and engine, skill name, SHA-256
version, load mode, observed UTF-8 bytes and observation basis. The server binds
identity to the durable session. Replay reuses the original transport receipt;
observing a replay does not manufacture another load.

The observer requires a namespaced Claude Skill call or the exact private file
path. It verifies the private file's SHA-256 against the bundled version and
recognizes complete bodies through plain, numbered and Hermes JSON file output.
Claude's native tool returns an acknowledgement; its namespaced success plus
verified plugin bytes establishes that load. A hash mismatch is a separate
`skill_reporting` record carrying the actual hash and size, never a confirmed
Blerg load. A failed verification reports `file_unavailable`. Hidden, failed and
partial reads do not establish a full load. `skill_reporting` states gaps separately;
installation is never counted as an on-demand load. Compact fallbacks record the
compact text's own version and size. `skill_applicability` reports successful
native file edits for code and interface files. Shell edits and invisible engine
reads are outside this observation seam. Current instrumentation covers the
bundled Blerg workflow skills, not arbitrary third-party skill catalogs.

The fifth Insights question, **Are my skills helping?**, uses the same owner and
workspace scope, filters, typography and disclosure pattern as the other views.
It separates loads, rule assessments and outcomes. Native subagent loads remain
under their containing conversation, and workspace totals include each owned
worker and channel once. Version details show review findings, recorded rework,
observed relevant edits, full-suite registrations, published images and reported
test failures before an observed edit. A registered full suite or an image alone
does not prove that the correct region ran exactly once, that a failure had the
intended meaning, or that screenshots were inspected at the required dimensions.
Missing evidence remains unknown. Cohort differences do not establish causation.

Agents read the same records using:

```
blerg-runner workspace skills --session SESSION
blerg-runner workspace metrics --json
blerg-runner workspace skill-evidence --skill ux-design --version HASH --rule say-once --result missed
```

Omit `--session` for the caller's owned workspace tree. A worker's credential
cannot inspect its siblings. `--card BOARD#N` uses the existing metrics filter.
Agent rule assessments are always declarations. The owner can record a rule
assessment through `POST /api/sessions/{id}/skill-evidence` using a human sign-in;
only that authenticated path can label a record as the person's correction.
The route accepts a bounded rule identifier, exact version and result, never a
skill text edit. No telemetry path modifies a skill. Main may propose changes,
and the person decides whether to change the text.

Reports keep the newest 5,000 records and state truncation. Evidence posts are
limited to 30 per session per minute under a database row lock, including across
replicas. A telemetry read failure is logged through `contracts/failurelog` and
marks only skill evidence unavailable; timing, spending and other Insights remain
available. Optional skill reads have their own two-second deadline, preserving
the caller's remaining budget for other evidence. The existing
`GET /api/insights/activity` remains human owner-only, including for administrators;
CLI reads use the existing own-session workspace metrics authorization.

Applicability credits a load only before the relevant edit in the same native
agent scope. Sessions whose inventory disables a skill are not scored as unused.
Outcome comparisons use explicit single-card links and exclude cards carrying
multiple observed versions. Version ordering follows first observed use in the
selected range, rather than a claimed source-edit date. Ordinary conversational
corrections are not automatically tagged: person-authored rule assessments use
the owner-authenticated endpoint above.

In the first review, the worker/channel shared prompt measured 23,428 bytes before and 11,454 after;
private paths brought a representative initial prompt to 12,029, or 15,614 with one
UX load. OpenClaw's compact skill text is 892 bytes for Main and 719 for workers;
its operational guide brought the total to 11,934 bytes for Main and 11,761 for
workers, before marker text. This adapter previously sent
no Blerg guide, so it has no positive before/after saving to claim. Restricted
skill text drops from 17,123/8,417 to 892/719 bytes.
If a worker loads all three bodies once, its total is 24,161 bytes: 733 bytes
above the old prompt. Layering does not promise savings for every workload.

Real-engine receipts are collected by opt-in `TestSkillRealEngine`; ordinary
unit runs skip it. A deliberately small read-only probe is not a production
typical-session estimate. Report its actual load count, including repeated reads,
and do not extrapolate savings to unseen workloads.

### Resume and delivery lifecycle

Each session has a stable private skill directory under its daemon state directory
(by default `~/.blerg-runner-daemon/skill-packs/<session hash>`). An explicit
engine home keeps its private state under that home. Hosts capture their state
directory when constructed; another daemon's startup cannot sweep it.
Container packs also derive
one path from that session identity. Driver replacement rebuilds the allowed
files at that path, removes disabled skills, and preserves the initial-delivery
receipt. Canceling a driver does not delete the pack: native resumed threads
still refer to its paths. Terminal session cleanup removes it after the driver
stops; daemon shutdown preserves recoverable state.

Every full and compact prompt includes the unconditional credentials, secrets,
authority, scope, coordination and reporting rules, independent of optional
skill switches. These rules are not hidden behind a skill load.

Initial inventory telemetry is released only after a successful completed turn
proves delivery, using that attempt's start time so compact context precedes
its edits. Failed or interrupted turns do not emit initial loads. The initial
record IDs are stable UUIDs per session and skill. A local receipt is kept after
all records are acknowledged; resume skips acknowledged inventory. Lost acks
may resend those same IDs, which the server deduplicates. On-demand reads still
produce a record per observed complete load. File paths are detected as
substrings in tool arguments, including shell quoting, redirects and punctuation;
content verification still requires the installed hash and complete output.

After acquiring the records lock, daemon startup sweeps owned, recognized packs
older than 24 hours. It preserves every recoverable session, recent directories,
foreign manifests and symlinks. This conservative age boundary also applies to
legacy `/tmp/blerg-skills-*` directories whose session ownership was never
recorded. Those recent legacy directories are not deleted speculatively.

`BLERG_TEST_SKILL_REAL_RESUME=1` extends the opt-in real-engine proof to stop and
replace each adapter, retain its native conversation identity, rebuild its pack,
and read the same skill path again without supplying a new path in the request.
It checks a prior-turn label as a continuity constraint. OpenClaw uses its
shipped transcript-replay recovery rather than a native session API.
The earlier byte figures above are measurements of the previous guide; the
always-on line and stable private paths add context. Report current real receipt
sizes when comparing this revision.

Host turn/resume proof does not establish sandbox or cluster operation, Codex or
Hermes native subagent skill reporting, or availability of OpenClaw's configured
default model. These require separate runtime acceptance evidence.

For older Codex conversations that already retained a random path, the first
accepted input after native resume also refreshes the always-on rules and current
private catalog. This avoids relying on ignored developer-instruction overrides.
It is a compact catalog refresh, not another injection of the full skill bodies.

### Production context budget and evidence boundaries

`TestSkillProductionContextBudget` caps the actual Main prompt plus its private
path guide at 13,312 bytes (13 KiB), using a fixed representative daemon state
path. This revision measures 13,116 bytes. The measured prompt includes the
production `skillCatalogEntry` descriptions; increasing either those descriptions
or the guide consumes the same budget. Longer installation paths add bytes;
this is a repeatable content regression budget, not a runtime path-length limit.

Only file-reader and shell tools can establish path-based skill observations.
Forwarding the catalog to an Agent or Task is not a read. Invalid telemetry and
pack creation/cleanup failures use bounded, cause-only failure diagnostics with
suppressed repeat counts; tool arguments, outputs and private paths are excluded.

Agent rule assessments must name a skill/version already loaded in that exact
session. Both daemon ingestion and the agent HTTP endpoint enforce this at the
same transactional append boundary. Owner corrections have a separate allowance
of 30 assessments per minute per session, so agent assessments cannot exhaust it.
Accepted receipt retries are idempotent even after that allowance is exhausted.
