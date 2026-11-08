# Testing audit: Blerg 0.6.0 (BLG-397)

Audit date: 2026-11-01. Source head: `995aff04663062a0bdf11c35c5b2f33e266749b8`.
Related work: BLG-298 (gate cost and scheduling), BLG-383 (follow-up context;
its card was not accessible). Size L, risk medium, complexity medium, as supplied.
Scope is documentation only: no product, test or gate changes. The scheduling
section below is an implementation specification, not a claim that the gate
already enforces layers.

## Findings and evidence quality

The database hypothesis is supported as a **dependency diagnosis**, not yet a
measurement of database-copy cost. Server policy matrices repeatedly create
private Postgres databases although their primary assertions concern recipients,
permissions, routing and serialization. Moving those decisions behind small
repository interfaces could remove most database dependence from these matrices.
Do not replace real SQL locking, migrations or transaction tests with mocks.

The retained runner trace measured 1,080.276 suite seconds, with 9,020 pass,
one fail and 134 skip test/subtest events. Server passed in 948.589 package seconds.
The failing test was
`daemon.TestActualFileRepliesShareShellControlAndDataScheduler`.
This trace is incomplete as a gate, and its failure is not characterized as a
flake. No rerun was made for this audit. Other slow packages do real kernel,
filesystem or timer work; a blanket database explanation is false.

A lexical call-graph scan of server test files found 2,163 named top-level
`Test*` functions in 358 files: 40 directly call `connectSrvTestDB`, and 1,548
reach it through named test helpers. Of 2,134 observed top-level passing events,
1,527 are linked to that helper. Their inclusive test times sum to 929.190 of
947.540 seconds (98.1%). This is **time in tests that use the fixture**, not time
spent copying databases. The scan follows identifier calls transitively within
test files; it is an approximation, ignores build-tag selection and method
resolution, and does not count fixture invocations inside loops. There are 52
lexical call sites outside the helper declaration, not 52 databases per run.

Evidence used:

| Retained profile | Suite work / outcome | Interpretation |
| --- | --- | --- |
| `gate-7ea52ccd-logs/trace` | runner-go 1,080.276 s; preflight 0.033 s | Incomplete; one runner failure |
| `gate-7ea52ccd-logs/vanilla` | 791.609 s elapsed, 13 selected suites completed | Selection completed, but runner unit and browser failed |
| `gate-995aff04-logs` | daemon-race 373.635 s; 2,527/0/102 P/F/S | Focused pass, not full gate |
| [BLG-298 record](pre-merge-measurements.md#per-suite-evidence) | completed split after profile: 61.41 min, 40 selected entries passed | Historical, different source/coverage; not a current benchmark |

The vanilla failures were runner-unit 2,737/5/15 and runner-browser 1,843/1/2.
Other measured vanilla test counts: board 351/0/0, chat 2,811/0/3.
The default local gate history file is absent in this worktree.
I reconstructed a temporary JSONL from the three retained summary files and ran
`scripts/gate_report.py` against it. Summary files lack commit/start metadata:
their directory labels are provenance hints, not verified commit identifiers.
Raw Go JSON logs provide the package and top-level test tables below.
No new Go, browser, performance, Docker or Postgres suite was needed or run.
No outside-session launcher was used by this audit. Historical tracing profiles
used the external launcher described in the BLG-298 record; their commands are
not new executions here.

Go package elapsed includes parallel children; parent/subtest elapsed overlaps.
Cached events can replay earlier durations. Never sum package/test rows into
gate wall time or call these controlled cold-cache measurements. Retained
slowest-30 summaries omit the rest; the ranked table uses complete runner JSON.
No process CPU, SQL CREATE/DROP timing or fixture-span telemetry exists here.

## Layer doctrine

| Layer | Boundary and required proof |
| --- | --- |
| 1 contract | Shared behavioral assertions against every implementation and library fake; wire schemas, status/error vocabulary, ownership and idempotency rules |
| 2 implementation | One implementation, including its real filesystem/kernel/browser where those are the implementation under test; deterministic policy cases use controlled clocks and inputs |
| 3a canned cross-service | Real client/transport/serialization against provider-owned canned HTTP/websocket services; no live database or remote service |
| 3b real services | Real Postgres, Docker or actual service dependency; keep migrations, SQL isolation, fences, unique constraints and runtime integration here |
| 4 end to end | Shipped components through public user paths across a complete local installation |
| 5 staging | Deployed desktop/cluster upgrade, real identity/provider and operational acceptance in a disposable environment |

For asynchronous recovery, a sent welcome or an open socket is not readiness.
Wait on the consumer's verified readiness acknowledgement, after capability and
identity state are installed. Keep a regression that requests work during the
half-recovered state and proves refusal, then proves admission after that exact
acknowledgement. This avoids using scheduling delays to hide publication races.

For outage isolation, exercise the entire lifecycle consumer (sync, status
reopening, reconnect and Stop), not only process survival or registration.
A daemon-local empty shadow must not invoke its remote reconciliation callback
or manufacture a remote custody receipt; test both the independent local path
and the separately retried, physically proved remote cleanup.

A package named `contracts` is not automatically layer 1. Nor does a real browser
make a component test end to end. Kernel containment tests are layer 2 OS
implementation tests, with a separate expensive-host capability. Canned Core
plus live Postgres is still 3b. Test filenames containing `g3b` are not evidence:
`server/cronfailurecard_g3b_test.go` also contains pure sanitizer cases (layer 2).

## Every gate suite

The table covers all current `matrix()` entries. Mixed suites retain the
highest service requirement until split; the listed lower layers describe
coverage inside the suite, not independently selectable jobs. Quality gates
are explicitly outside behavioral layers. Times marked historical are the
BLG-298 split after profile; blanks mean no comparable measurement retained.

| Suite | Current layers | Dependency / timing concern | Seconds |
| --- | --- | --- | ---: |
| `host-tracing-preflight` | capability gate | Linux ptrace; run only before OS suites | 0.016 retained |
| `contracts-vet` | quality gate | No service; static/tool validation | 0.335 historical |
| `contracts-go` | 1 / 2 / 3a | Contract assertions, local OS and canned HTTP | 3.308 historical |
| `core-vet` | quality gate | No service; static/tool validation | 0.571 historical |
| `core-go` | 1 / 2 / 3a / 3b | Real Postgres mixed with policy and canned service peers | 129.165 historical |
| `board-vet` | quality gate | No service; static/tool validation | 0.515 historical |
| `board-go` | 1 / 2 / 3a / 3b | Real Postgres mixed with policy and canned service peers | 135.635 historical |
| `runner-vet` | quality gate | No service; static/tool validation | 4.823 retained |
| `runner-go` | 1 / 2 / 3a / 3b | Postgres, Docker, OS, canned peers; opt-in acceptance | 1080.276 retained |
| `runtime-smoke-local` | 3b (partial E2E) | Real daemon, HTTP/websocket, Postgres, Docker; canned Core/engine | — |
| `daemon-race` | 2 / 3a / 3b | Real processes/ptrace/timers; daemon includes Docker | 373.635 retained |
| `runtime-race` | 1 / 2 | Real processes/ptrace/timers | 304.225 historical |
| `persistent-shell-wire-go` | 3b | Real server/daemon/helper, Postgres, ptrace; virtual absence clock | 3.659 historical |
| `persistent-shell-wire-race` | 3b | Real server/daemon/helper, Postgres, ptrace; virtual absence clock | 5.470 historical |
| `lint-pin` | quality gate | No service; static/tool validation | 0.225 historical |
| `gofmt` | quality gate | No service; static/tool validation | 1.016 historical |
| `contracts-go-lint` | quality gate | No service; static/tool validation | 1.116 historical |
| `core-go-lint` | quality gate | No service; static/tool validation | 1.371 historical |
| `board-go-lint` | quality gate | No service; static/tool validation | 1.355 historical |
| `runner-go-lint` | quality gate | No service; static/tool validation | 42.869 retained |
| `core-lint` | quality gate | No service; static/tool validation | 9.550 historical |
| `core-types` | quality gate | No service; static/tool validation | 4.271 historical |
| `core-tests` | 2 / 3a | DOM/model logic and canned requests; no deployed stack | 8.483 historical |
| `core-build` | quality gate | No service; static/tool validation | 5.024 historical |
| `board-lint` | quality gate | No service; static/tool validation | 17.673 historical |
| `board-types` | quality gate | No service; static/tool validation | 10.777 historical |
| `board-tests` | 2 / 3a | DOM/model logic and canned requests; no deployed stack | 27.231 retained |
| `board-build` | quality gate | No service; static/tool validation | 13.972 historical |
| `chat-lint` | quality gate | No service; static/tool validation | 22.637 retained |
| `chat-types` | quality gate | No service; static/tool validation | 12.546 retained |
| `chat-tests` | 2 / 3a | DOM/model logic and canned requests; no deployed stack | 93.427 retained |
| `chat-build` | quality gate | No service; static/tool validation | 10.678 retained |
| `runner-lint` | quality gate | No service; static/tool validation | 42.464 retained |
| `runner-types` | quality gate | No service; static/tool validation | 0.508 retained |
| `runner-unit` | 2 / 3a | DOM/model logic and canned requests; no deployed stack | 122.533 retained |
| `runner-browser` | 2 / 3a | Real layout/browser; canned APIs, performance requires idle host | 323.687 retained |
| `runner-performance` | 2 / 3a | Real layout/browser; canned APIs, performance requires idle host | 87.621 historical |
| `runner-build` | quality gate | No service; static/tool validation | 36.836 retained |
| `cli-unittest` | 2 / 3a | Python CLI subprocesses and canned HTTP, no real Board | 193.801 historical |
| `cli-pytest` | 2 / 3a | Python CLI subprocesses and canned HTTP, no real Board | 20.192 historical |
| `desktop-scripts` | 2 / 3a | Mocked tools/installers, fixture git repos; Compose validates config, not live install | — |
| `scrub` | quality gate | No service; static/tool validation | — |
| `links` | quality gate | No service; static/tool validation | — |
| `scripts` | 2 / 3a | Mocked tools/installers, fixture git repos; Compose validates config, not live install | 51.069 retained |


Layer 4/5 coverage lives outside the ordinary matrix: the
[session acceptance campaign](session-e2e.md), real-install engine checks and
[cluster acceptance controller](../install/k8s/acceptance/RUNBOOK.md).
`runtime-smoke-local` is not complete layer 4: Core identity and engine are
fixtures. Cluster smoke without controller flags can skip; it is not staging
coverage. Real-engine paid checks remain opt-in. Release upgrades require real
desktop/Kubernetes evidence, not mocked installer success.

## Package and source inventory

There are 1,180 Go test files in 97 package directories. Static marker counts
below are **files containing a match**, not test executions or proven resource
use. Comments and runtime strings can match; helper indirection can hide a
dependency. In particular, `runtime="docker"` is not a Docker launch.
The matrix above gives execution boundaries; this inventory identifies where
to inspect before splitting them.

| Module | Test files | Postgres markers | Sleep | Clock/timer | Docker text | Named fake/stub/mock type |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| contracts | 29 | 0 | 3 | 5 | 2 | 1 |
| core | 108 | 38 | 9 | 27 | 1 | 4 |
| board | 123 | 23 | 8 | 24 | 8 | 6 |
| runner | 920 | 108 | 152 | 439 | 148 | 25 |

Marker expressions: Postgres = `pgx|DATABASE_URL|testsupport.(Open|Pool|Blank)|connectSrvTestDB(`;
sleep = `time.Sleep(`; clock/timer = `time.(Now|After|NewTimer|NewTicker|Since)(`;
Docker = case-insensitive `docker`; fake = top-level type name starting
`fake`, `stub` or `mock`. Dots/parentheses are literal in the scan.
Named fake types undercount provider fixtures such as `authoritativeProvider`.

Package placement (paths relative to each module, grouped exhaustively;
mixed packages must be split at test boundaries before gate selection changes):

| Module | Packages | Current placement |
| --- | --- | --- |
| contracts | agentsmanifest, boardfiling, childlimits, engineaccess, engines, health, identity, logsafe, netguard, pluginspec, releasegate, runtimelifecycle, secrets, sessionstorage, shellpolicy, testfixture, theme, updatenotice | 1/2; canned HTTP in client cases is 3a |
| contracts | cmd/blerg-upgrade, desktopupgrade | 2/3a; installer/entrypoint fixtures, not a real upgrade |
| core | cmd/blerg-core, internal/api, internal/authprovider, internal/credentials, internal/db, internal/discovery, internal/identity, internal/mcpconn, internal/projects, internal/testsupport | Mixed 2/3a/3b; real Postgres cases require 3b |
| core | internal/keybackend, internal/landing, internal/plugins, internal/releasenotes, internal/signing | 2/3a; keybackend real Vault checks are opt-in 3b |
| board | cmd/blerg-board-server, internal/api, internal/db, internal/gate, internal/mcp, internal/testsupport | Mixed 2/3a/3b, SQL-backed API/workflow tests remain 3b |
| board | internal/auth, internal/chatproxy, internal/coreauth, internal/corecred, internal/coremint, internal/localinfer, internal/runner | 2/3a client/auth implementations and canned providers |
| board | internal/escalate, internal/secretbox, internal/templates, prompts | 2 |
| runner | cmd/server, internal/cron, internal/db, internal/mcp, internal/mcpgw, internal/server, internal/testsupport | Mixed 2/3a/3b; SQL/API integration |
| runner | internal/daemon, internal/shellruntime | 1/2/3a/3b; OS/process, canned peers and real Docker; acceptance cases need individual 4/5 manifests |
| runner | internal/ownershell, internal/processcontainment, internal/processtelemetry, internal/clusterworkspace, internal/hostgit, internal/pluginsinstall, internal/workbranch | 1/2 OS/filesystem; real kernel proof is not replaceable by a fake |
| runner | internal/agent, internal/agent/anthropic, internal/agent/tools, internal/coreauth, internal/discovery, internal/gitprovider, internal/models, internal/runner, internal/ticket, internal/workspacecli | 2/3a; canned HTTP/command boundaries |
| runner | cmd/cluster-failure-test, cmd/daemon, cmd/runtime-smoke, internal/activity, internal/agent/skills, internal/checklist, internal/codexauth, internal/commandsteps, internal/fileview, internal/gitremote, internal/idempotency, internal/livestate, internal/protocol, internal/scratch, internal/topics, internal/updatehandoff, internal/workspacepolicy, internal/workspaceselection, skills | 1/2 implementation/contract helpers; command stubs do not certify the deployed runtime |

These are boundary classifications rather than a claim that every assertion in
a mixed package shares one layer. All three module `internal/testsupport`
packages are 3b for database creation/sweeping, with pure naming/policy tests in 2.

## Slowest packages and tests

Retained runner-go package results, sorted by package elapsed:

| Package under runner | Seconds | Outcome |
| --- | ---: | --- |
| internal/server | 948.589 | pass |
| internal/processcontainment | 298.662 | pass |
| internal/daemon | 293.768 | fail |
| internal/db | 112.540 | pass |
| internal/shellruntime | 86.015 | pass |
| internal/mcpgw | 68.190 | pass |
| internal/clusterworkspace | 41.400 | pass |
| internal/cron | 16.786 | pass |
| internal/testsupport | 11.343 | pass |
| internal/mcp | 10.835 | pass |

Ranked migration candidates below use measured inclusive **top-level** elapsed.
Move policy assertions down while retaining a small real adapter/transport proof.
No claimed seconds saved until the replacement suite is measured.

| Rank | Package.Test | Seconds | Proposed split / target |
| ---: | --- | ---: | --- |
| 1 | daemon.TestInterruptedWaiterRetriesUntilAcknowledged | 35.010 | 3a to deterministic 2 retry policy plus one canned HTTP 3a; inject timer scheduler, retain durable replay |
| 2 | clusterworkspace.TestClusterRefusedDeepMemberStillAccountsAggregate | 34.220 | 2 OS to 2 deterministic traversal accounting; synthesize 500,000-entry stream, retain small real hard-link/depth boundary proof |
| 3 | server.TestDesktopPersonalAttachPolicyTimeout | 16.010 | 3b to 2 deadline policy + 3a attach flow; retain 3b advisory-lock release/generation test |
| 4 | server.TestP6MemberResultsRecipientAndDelivery | 15.740 | 30-case 3b matrix to 2 recipient policy + 3a serialized delivery; retain SQL ownership reassignment/fence cases |
| 5 | server.TestLaunchAmbiguousSecretCleanup | 14.500 | 3b to 3a canned Kubernetes plus virtual deadline; keep SQL runtime-claim and live Kubernetes UID-precondition coverage |
| 6 | server.TestQuestionDestructivePhrasesOnly | 13.720 | 21-case 3b matrix to 2 classifier table, keep representative public HTTP route 3a and persistence 3b |
| 7 | daemon.TestWorkspaceSearchLargeIndexUnderContention | 12.320 | Separate index/query logic 2 from measured real filesystem contention; retain explicit resource/performance job |
| 8 | server.TestAuthoritativeNativeAskQueuesEveryOrdinarySource | 11.990 | 14-case 3b to 3a real agent loop/HTTP with memory session repository; retain SQL durability proof |
| 9 | server.TestDelegationMatchMatrix | 9.820 | 3b to 2 selection policy using typed inventory; 3a serialization and 3b grants separately |
| 10 | server.TestP6PrivateHTTPDeliveryFailures | 8.880 | 3b to 3a scripted delivery failures, plus SQL recipient/ownership tests |
| 11 | server.TestPostSessionsRejectsBadModelOrEffort | 8.540 | 3b to 2 model/effort validation table and representative authenticated HTTP 3a |
| 12 | server.TestResolveGrantRefusals | 6.260 | 3b refusal policy to 2/3a with provider-owned auth/grant fake; real grant revocation stays 3b |

The first six total 129.200 inclusive seconds, not a projected gate saving.
The server seven candidates ranked 3–6 and 8–10 include repeated private fixture
creation, but direct SQL mutations and ownership fences mean they are not
mechanically replaceable by an in-memory pgx imitation.

Keep these expensive layer 2 OS proofs, even when adding faster policy cases:

| Test in runner | Seconds | Why a fake cannot replace the proof |
| --- | ---: | --- |
| processcontainment.TestTIDReuseAcrossPendingExec | 60.670 | Actual thread identity/reuse and exec tracing |
| processcontainment.TestTreeAccountingMatchesKernelAfterSpawnStorm | 50.120 | Kernel accounting after process churn |
| processcontainment.TestFreezeHoldsThroughChaos | 46.420 | Scheduling, signals and stopped-tree containment |
| shellruntime.TestRealHostFullThirtySecondOfflineGrant | 29.930 | Real elapsed expiry boundary; virtual-clock lease tests complement it |
| processcontainment.TestNewChildRunsNothingBeforeItsContinue | 23.770 | Child cannot execute before tracer permission |
| processcontainment.TestGroupSurvivesEngineKillingItsOwnShells | 20.020 | Adversarial process lifecycle |

## Database hypothesis: next experiment

`runner/internal/testsupport/database.go` migrates a template once per key and
copies it with `CREATE DATABASE ... TEMPLATE ... STRATEGY WAL_LOG` per Open.
`connectSrvTestDB` then opens another pool with a private search path.
Core and Board have similar template/copy/cleanup helpers. Thus migration is
already amortized; repeated cloning, pool setup, fixture inserts and cleanup
remain. Cutting migrations again will not address every per-case cost.

Example: `newDelegFx` calls `runnerContractPool`, creates API/auth/hub/desktop
and parent-session state. The phrase-only table creates it 21 times, while
`TestAuthoritativeNativeAskQueuesEveryOrdinarySource` creates it 14 times.
`TestP6MemberResultsRecipientAndDelivery` has 6 endpoints × 5 ownership states.
These are strong places to measure a replacement, not proof that copying alone
accounts for the elapsed time.

Before changing tests, instrument owned fixture phases (template migrate,
clone, pool connect, seed, body, close/drop) in a separate follow-up. Measure
one named matrix with normal cache handling, recording cached status and
database creation count. Compare the same behavioral cases against a typed
repository fake; retain one real public HTTP + Postgres case for each SQL
invariant. Report median and spread across justified fresh timing runs,
Postgres version/storage settings and package concurrency. Never share one
mutable database across tests or weaken fixture cleanup to obtain a speedup.
Do not infer copy time from the 98.1% fixture-linked elapsed fraction.

### Owned-fixture timing experiment (BLG-397 follow-up)

Measured on 2026-11-01, normal non-race server tests, without tracing or
`outside.sh`. `BLERG_TEST_FIXTURE_TIMING=1` enables phase logs in runner's
testsupport and delegation/privacy server fixtures. The switch is read once
per process. Off means no per-phase environment lookup, clock read, allocation
or log; the remaining work is an inlined disabled branch/zero timer. This is
not a claim of literally zero CPU instructions. No fixture or cleanup policy
changes when timing is enabled.

Three sequential fresh package processes per version provide an initial median
and min–max spread, rather than a confidence interval. Test result caching was
deliberately bypassed with `-count=1` for these timing samples only; normal Go
build caches were retained. No `(cached)` result was included. Two preliminary
samples overlapped briefly and were discarded. The retained runs had one server
test process at a time, serial matrices (no `t.Parallel`), default package/test
concurrency, no concurrent heavy suite. Brief focused helper checks and a Go
compile occurred during collection; these are normal development measurements,
not an isolated performance gate or controlled hardware benchmark.

Postgres: 16.15, Debian x86-64; `fsync=on`, `synchronous_commit=on`,
`full_page_writes=on`, `shared_buffers=128MB`, `max_connections=100`.
The checkout filesystem was ext4. The database server's backing storage and
cache warmth were not independently verified; no storage setting was changed.
Results apply to this test server, not an extrapolated CI installation.

Phase definitions: `template_migrate` includes template CREATE, ownership mark,
pool construction, migrations and migration-pool close; `clone` measures only
the successful CREATE DATABASE statement. `pool_connect` and
`server_pool_connect` measure pgx pool construction/configuration, **not** a
physical connection: pgx connects lazily, so the first connection lands in seed.
`seed` spans the shared fixture constructor after database opening; `body`
spans its return until its cleanup callback, including test-specific inserts,
requests and assertions. Close times cover the two pools separately. `drop`
includes its administrative connection and DROP. These phases deliberately do
not claim exhaustive accounting: admin/run-lease setup, clone ownership marking,
configuration, auth-server cleanup and final process template cleanup remain
outside them. Template cleanup still runs and affects package elapsed time.
Do not sum independently calculated medians as an exact total.

The original phrase table had 7 ordinary and 14 destructive/normalized phrases.
All 21 now run directly against `destructiveQuestion` at layer 2, without a
repository fake: the classifier has no repository dependency. A typed session
repository seam would be unrelated product design for this particular subject.
One retained layer-3b test, `TestQuestionDestructivePhrasesHTTPPostgres`, sends
all 21 phrases through actual loopback HTTP, bearer authentication, JSON and
Postgres using **one fixture owned by that test**. Every original 201-status
and parent/person-recipient assertion remains, and persisted `PersonRequired`
is additionally checked. No phrase case was dropped. HTTP status, auth, JSON,
routing and SQL persistence cannot move to the pure classifier; those meanings
remain in the integration case. Requests within that case share its state;
separate tests still never share a mutable database. This measures both layering
and eliminating repeated fixture setup, not the isolated gain of classifier
calls alone. It also strengthens the old direct-handler transport proof.

The selected pair originally created 51 clones + 1 template per process;
afterward it creates 31 clones + 1 template. The classifier creates zero;
the phrase integration case creates 1 clone + 1 template (it runs first), and
P6 still creates 30 clones, reusing that process's template. Counts are successful
clone/template phase receipts, not guesses from test names. No extra templates
are credited to P6. A standalone P6 process would build its own template.

Reproduce from `runner` with the configured dedicated `TEST_DATABASE_URL` and
`GOWORK=off`. Baseline source is instrumentation commit `49fd9215`; the next
commit contains the rewrite and these results:

```sh
# Before the rewrite (instrumentation present): three separate invocations.
BLERG_TEST_FIXTURE_TIMING=1 go test ./internal/server -run '^(TestQuestionDestructivePhrasesOnly|TestP6MemberResultsRecipientAndDelivery)$' -count=1 -json
# After the rewrite: three separate invocations, same order and database server.
BLERG_TEST_FIXTURE_TIMING=1 go test ./internal/server -run '^(TestQuestionDestructivePhrasesOnly|TestQuestionDestructivePhrasesHTTPPostgres|TestP6MemberResultsRecipientAndDelivery)$' -count=1 -json
```

The temporary analysis tool was Go, not Python/shell: decode `go test -json`,
sum `fixture_phase=PHASE ns=N` by top-level test within each process, count each
phase receipt, then sort the three per-process totals and report middle/min/max.
Top-level `pass.Elapsed` measures test time including its test cleanups; package
`pass.Elapsed` additionally includes TestMain template/run-lease cleanup.
Compilation and Go-command startup are excluded from those elapsed fields.

Seconds below are median [min, max], with phase values summed over the cases in
each process before calculating statistics:

| Measurement | Original phrase matrix | Layer-2 table + one HTTP/SQL case | P6 original | P6 control after |
| --- | --- | --- | --- | --- |
| Test elapsed | 10.380 [9.840, 11.220] | 3.160 [3.090, 3.350] | 15.040 [14.770, 16.650] | 14.880 [14.490, 14.930] |
| Template build/migrate | 1.318 [1.247, 1.456] | 1.339 [1.297, 1.460] | reused | reused |
| Clone | 1.850 [1.664, 2.004] | 0.084 [0.074, 0.094] | 2.666 [2.605, 3.357] | 2.523 [2.492, 2.566] |
| Shared seed | 1.131 [1.026, 1.218] | 0.077 [0.074, 0.081] | 2.091 [2.043, 2.546] | 1.989 [1.963, 2.045] |
| Body (includes case-specific seed) | 2.461 [2.307, 2.661] | 1.244 [1.201, 1.257] | 6.477 [6.441, 6.712] | 6.410 [6.394, 6.425] |
| Drop | 3.417 [3.292, 3.610] | 0.401 [0.379, 0.423] | 3.571 [3.282, 3.646] | 3.583 [3.341, 3.645] |
| Clone count | 21 | 1 | 30 | 30 |

Small phases, in **milliseconds**, same median [min, max] convention:

| Phase | Original phrase | Phrase HTTP/SQL after | P6 original | P6 after |
| --- | --- | --- | --- | --- |
| Base pool construction | 0.770 [0.750, 0.865] | 0.038 [0.035, 0.041] | 1.195 [1.147, 1.208] | 1.114 [1.085, 1.142] |
| Server pool construction | 0.094 [0.084, 0.190] | 0.003 [0.003, 0.003] | 0.218 [0.217, 0.264] | 0.136 [0.131, 0.167] |
| Base pool close | 0.411 [0.311, 0.439] | 0.026 [0.025, 0.026] | 0.578 [0.338, 0.721] | 0.583 [0.524, 0.611] |
| Server pool close | 1.888 [1.383, 2.104] | 0.116 [0.078, 0.118] | 2.187 [1.796, 3.390] | 2.575 [2.267, 2.822] |

The pure table's elapsed field rounds to 0.000 s in all three JSON samples;
this is below the timer's reporting resolution, not a claim of zero runtime.
The table plus retained integration case reduces the observed median phrase
time by **7.220 s (69.6%)**, retaining all original assertions. P6's 0.160 s
median movement has overlapping ranges and no code change; credit it to no
optimization. This is not a measured full-runner or full-gate saving.

Next work, ranked by evidence rather than promised savings:

1. Keep the phrase rewrite: the only implemented, measured behavioral replacement
   here saves 7.220 s of median test time and 20 database creations per selected
   run. No new repository seam is needed for a pure classifier.
2. Investigate P6's body first (6.477 s median before, still 6.410 s after),
   separating its case-specific SQL setup, delivery and waiting. Its transport,
   recipient revalidation and serialization assertions cannot safely become a
   classifier table. A typed provider-owned repository seam needs its own design
   and contract proof. No replacement or saving for P6 was measured.
3. For fixture mechanics, investigate drop before clone: original P6 spends
   3.571 s dropping and 2.666 s cloning, versus 2.091 s in common seed. Those
   are measured costs to investigate, **not recoverable savings**. Preserve
   per-test databases, ownership marks, leases and cleanup; do not disable
   fsync, reuse mutable databases across tests or omit drops.
4. Defer pool-constructor micro-optimization (about 1.4 ms total for P6) and
   further migration amortization (already one template). Physical connection
   costs have not been separated from seed. No measured evidence supports a
   new database-copy strategy, a repository fake for the whole server, or an
   extrapolated suite speedup yet.

Follow-up validation (all Go commands in `runner`, `GOWORK=off`, configured
test database; normal result/build cache handling except timing samples):

```sh
go test ./internal/testsupport -json
go test ./internal/server -run '^Test(QuestionDestructive|RequiredChildQuestionTellsParent|ChildOwnerOnlyQuestion|P6MemberResults)' -json
BLERG_TEST_FIXTURE_TIMING=1 go test ./internal/testsupport -run '^TestFixtureTiming' -v
"$GOLANGCI_LINT" run --config ../.golangci.yml ./internal/testsupport/... ./internal/server/...
go build ./internal/testsupport ./internal/server
# From the repository root:
./scripts/scrub.sh
git diff --check
```

Testsupport: 12 top-level passes, 9 subtest passes, 1 intentional skip (enabled
timer test in the default-off run). Focused server: 5 top-level passes and
54 subtest passes; no skips, failures or phase logs when disabled. Opt-in helper
check: 1 pass, 1 intentional skip (disabled-only test). Each retained baseline
timing sample passed 2 top-level tests / 51 subtests; each after sample passed
3 / 51, plus the integration case's 21 HTTP requests. Lint: 0 issues; regional
build, scrub and whitespace checks passed. No full suite, browser, race or
performance gate was run, and no independent reviewer was used.
An additional optional repository-wide `./scripts/check_links.sh` check was
stopped after more than two minutes without output (termination exit 143).
It is incomplete, not a passing link check; no new Markdown links were added.

TDD evidence: initially missing timer symbols produced an expected compile
failure, which is not behavioral red evidence. A temporary mutation returning
a zero timer with timing enabled then failed `TestFixtureTimingEnabled` for
the intended missing start; restoration passed. Returning false from the real
classifier failed all 14 destructive subcases of the pure table (including
both Unicode cases); restoration passed. These intentional failures were
removed before validation. No unexpected test failures occurred. Default-off
timing checks include zero allocations for phase start/end/body cleanup and a
zero start time. No containers/images or ptrace tracer invocations were created
or used. Temporary Go analysis/edit programs and raw logs were removed after
recording these results; normal fixture cleanup removed owned databases.

Open questions for subsequent work: what portion of P6 body time is seed,
delivery or waiting; what storage backs this test server; and what typed
repository contract can retain P6's recipient revalidation proof? None blocks
this bounded experiment, and none has a measured replacement gain yet.

## Provider-owned library fakes and their contracts

No `boardtest` package exists in this checkout (source search).
“boardtest style” here means a provider-owned, importable test library with a
single contract suite shared by fake and real adapter. Do not establish another
consumer-owned map that silently diverges from the service.

| Proposed library / ownership | Consumer fixtures to replace | Contract suite (layer 1) and real proof |
| --- | --- | --- |
| Board-owned `boardtest` HTTP service | server `fakeCards`, `fakeSessionBoard`, `fakeBoardAPI`, `fakeBoardExchanger`; CLI canned Board handlers | Public routes, bearer scopes, archived/not-found distinctions, version conflicts, dedup refresh, proposal/held/denied response bodies and pagination; run same assertions against real Board HTTP + Postgres in 3b |
| Core-owned `coretest` | `newTestCoreServer`, `fakeCronCore`, `fakeCronMinter`, `fakeVault`, desktop auth handlers; Board's Core client fixtures | JWKS rotation, expiry/revocation, audience/capability refusals, internal-key auth, credentials and no-secret errors, 404/redirect refusal; real Core HTTP/SQL and opt-in Vault adapter in 3b |
| Runner-owned typed `sessionstoretest` | `newDelegFx` / `newPrivFixture` SQL setup in policy matrices | Ownership, privacy, not-found, generation CAS, immutable workspace results, question recipient/state transitions, idempotent reply and replay; same suite for memory and Postgres adapters, plus SQL-only lock/transaction tests in 3b |
| Runner-owned `runnertest` wire peer | ad hoc daemon/runner HTTP and websocket handlers across CLI, Board and server | Message/answer receipts, reconnect cursor, terminal statuses, error envelopes, stop authorization and credit frames; contract over real serialization, then shipped daemon/server smoke in 3b |
| Runner agent `agenttest` | `authoritativeProvider`, `fakeProvider`, ad hoc scripted streams | Ordered stream/done/tool events, cancellation, usage, malformed frames, tool result correlation and no late events; adapter canned-wire tests 3a, real provider staging opt-in |
| Kubernetes adapter `kubetest` | server `fakeK8s` and custom Secret handler in launch review tests | ResourceVersion/UID preconditions, POST accepted-but-reply-lost, late visibility, 403/404/409, deletion timestamps and retry; canned HTTP 3a, actual API/RBAC 3b or staging 5 |
| Git/vault adapter fixtures | consumer `fakeProvider` / `fakeVault` in gitproviders tests | Provider errors, auth redaction, URL/ref normalization, cancellation; adapter protocol tests 3a, disposable repo/real vault proofs separate |

Keep these narrowly scoped. A session repository seam requires product design:
the current API passes concrete `*pgxpool.Pool` and directly calls db functions.
This docs-only audit does not pretend that a fake can be injected today.
Contract tests must exercise public client transport and decode actual JSON,
not just compare fake method results. Seed fixtures must be explicit, isolated,
and fail on unexpected routes/calls. Memory fakes need controllable clocks,
errors, delivery acknowledgments and lifecycle cleanup.

## Missing OS and time seams

| Boundary | Existing evidence / missing seam | Safe target |
| --- | --- | --- |
| Durable retry timer | waiter_notice uses real exponential timers; three refusals consume about 35 s | Inject timer factory/scheduler into retry worker; advance 5/10/20 s and assert acknowledgments, retain real HTTP cancellation test |
| Attach/launch deadlines | policy attach waits three real deadlines; Secret cleanup explicitly tests actual ten-second deadline | Clock/deadline factory and observable completion hooks; production default unchanged; retain one real deadline/fence proof |
| Directory traversal/accounting | deep aggregate test builds 500,000 hard links and a sparse 11 GiB file | Iterator/stat seam for entry/depth/overflow policy; retain real Linux no-follow/mount/hard-link/openat behavior |
| Process observation | /proc, wait/ptrace identity, signals and exec are inseparable in containment proofs | Typed process-observer snapshots for state-machine cases; keep real tracer tests as mandatory OS capability jobs |
| Command lifecycle | daemon tests launch Git, shells, tmux, helper binaries and Docker | Narrow command runner with structured exit/signal/stream outcomes for policy tests; keep real process-group cleanup/security proof |
| Async completion | polling with Sleep and Now appears in server question/recovery tests | Explicit receipt/condition channels and cancellable waits; retain wall-clock bounds as safety guards, not as progress signals |
| SQL clock | known Core token tests compare DB and local time | Shared injected decision time where legal; real DB expiry/clock-skew tests remain 3b |
| Browser scheduling | layout and typing latency require real rendering | Unit fake timers for debounce/state; retain real browser scroll/layout and isolated performance budgets |

Existing seams should be reused: persistent shell has test-build-only clock
support, ownershell lease clocks and watchdog resolvers. The tagged wire test
already simulates 31 minutes of absence. Extend that pattern without exposing a
security clock override to production. Do not turn kernel security assertions
into fake-only tests.

## When pre_merge.py should run each layer

Current behavior is cost-first within a selected matrix: tracing preflight
first; runner-go/daemon-race/runtime-race get priority; browser performance waits
until all ordinary work exits. There is no layer metadata or layer barrier.
`--changed` selects complete regional suites, while `--changed-since` is
focused verification. Mixed Go packages prevent honest layer filtering.

Proposed follow-up to BLG-298/383, in dependency order:

1. Add explicit suite metadata: layer(s), required capabilities (Postgres,
   Docker, ptrace, browsers, network), exclusivity and measured cost. Produce
   a complete test-to-layer manifest for mixed packages; reject unmapped tests
   rather than deriving a layer from a filename suffix.
2. Separate deterministic policy/repository contracts from SQL/OS adapter
   tests. Start with the ranked matrices. Until this exists, preserve full
   mixed-suite coverage at its current cadence; never exclude DB tests by
   clearing environment variables and count the resulting skips as passes.
3. During development run affected layer 1 and 2 tests plus quality gates;
   then affected 3a client/provider contracts. Select touched 3b or OS tests
   immediately for SQL, runtime, containment or adapter changes.
4. Before merge run all selected regional 1/2/3a work, then required regional
   3b and expensive OS proofs. A contract/shared-input change widens to all
   consumers. Lower-layer failure should prevent admission of later expensive
   stages while retaining logs and explicit not-run outcomes. Resource
   exhaustion still stops admission and drains running work.
5. Run layer 4 local-install acceptance before release and when install/login/
   orchestration contracts change; layer 5 belongs to disposable staging and
   upgrade/release approval. Keep the existing required local smoke until the
   coordinator explicitly adopts a replacement cadence; do not silently remove
   it because this audit calls it partial E2E.
6. Preserve locks, bounded worker/memory admission and the final isolated
   browser performance phase. Within a layer schedule measured longest work
   first; run capability preflight immediately before the suites needing it,
   so missing ptrace does not prevent useful deterministic feedback.
7. Retain existing race guarantees: selected runtime/daemon coverage before
   merge, three daemon passes on release. Record skip/missing receipts as
   incomplete and keep `full_matrix`, selected coverage and release evidence
   distinct. A layer-2 pass is never a replacement for SQL or kernel proof.

Acceptance for that follow-up: list mode prints layer/capability/reason for
every selected and excluded suite; no test is lost or duplicated across
partitions; required public wire receipts remain checked; a lower-layer
failure demonstrably prevents expensive admissions; release retains all layers
and required staging evidence. Measure before/after at identical coverage and
resource budgets. A 20-minute target remains unverified.

## Reproduction and validation

Read-only measurements used Python 3 to parse retained `summary.json` and
`runner-go.log`, and:

```sh
# Rebuild a local JSONL from retained summaries if history is absent.
python3 - <<'PY'
import json
from pathlib import Path
records = []
for path in sorted(Path.home().glob('.cache/gate-*/**/summary.json')):
    record = json.loads(path.read_text())
    record['id'] = str(path.parent.relative_to(Path.home() / '.cache'))
    records.append(record)
Path('/tmp/testing-audit-telemetry.jsonl').write_text(
    ''.join(json.dumps(record) + '\n' for record in records))
PY
PYTHONDONTWRITEBYTECODE=1 python3 scripts/gate_report.py /tmp/testing-audit-telemetry.jsonl --run 0
# Repeat --run N for the remaining records; do not mix them into a green gate.
./scripts/scrub.sh
./scripts/check_links.sh
git diff --check
```

Package/test ranking: decode Go JSON lines, select terminal actions
`pass/fail/skip`; package rows have no `Test`, top-level test rows have no slash
in `Test`. Sort on `Elapsed` descending. Count terminal events, not output lines.
The server call scan splits named test/helper functions at the next named
function, collects identifier-call sets and repeatedly adds functions reaching
`connectSrvTestDB`. It is lexical evidence, not runtime tracing.

For the original read-only audit, no product/test code changed, so no new product
test counts, lint/build results or full-gate pass were claimed. No
containers/images/databases were created by that original audit. The fixture
follow-up above records its own database experiments and validation separately.
Temporary reconstruction files and Python bytecode created by inspection are
removed after validation. Board read was refused and `board access` confirmed
no readable board, so card status/estimates and related-card details were not
changed. Open questions for the coordinator: who owns the repository seam and
provider libraries, and which release environment supplies layer 5 evidence?

## BLG-397 follow-up: ranks 1, 2 and 11 (0.6.0)

This bounded rewrite starts at main `54f2558a`. Only the three requested ranks
were selected; no additional candidates or suite-wide savings are claimed.
The owning packages now supply an unexported retry wait function and a
`budgetDirectory` traversal interface. Production defaults still use real
cancellable timers and descriptor-relative `openat2`/no-follow/no-cross-mount
opens plus `Fstatat(AT_SYMLINK_NOFOLLOW)`. Traversal owns and closes each opened
directory, including depth-refusal exits. No production clock override or
filesystem security bypass is exposed.

Proof mapping:

- Rank 1: the deterministic retry test supplies canned HTTP responses and a wait
  function. Three transient failures must schedule exactly 5/10/20 seconds,
  retain the durable file until acknowledgment, attempt four deliveries, then
  remove the file. A separate real loopback HTTP `CloseAsk` case checks poll
  cancellation, POST path, bearer header and `waiter_ended` JSON. The real timer
  adapter has canceled-context and ready-timer checks. Existing durable restart,
  terminal/transient-response and bounded-batch tests remain unchanged.
- Rank 2: both byte and entry exhaustion still return `errAggregateBudget` after
  passing the member depth limit, and still block both members in
  `ValidateRound`. A bounded synthetic directory stream supplies 130 descendant
  directories and either an 11 GiB stat or 500,001 payload entries; the test also
  explicitly requires the deep member's `ErrPlan` refusal. The small real Linux
  proof traverses the same depth, first accounts a 6 GiB sparse file despite the
  member refusal, then adds one hard link: both logical names must count and
  exhaust the aggregate byte budget, blocking both members. This preserves real
  traversal/stat/hard-link integration without creating 500,000 links. Existing
  mount, unreadable-directory, depth-ceiling and tampering proofs remain intact.
- Rank 11: all 14 original invalid inputs call the existing `modelRuleProblem`
  directly. One representative authenticated loopback HTTP request retains the 422
  and no-daemon-spawn assertions. Thus rejection vocabulary moves to layer 2;
  auth, JSON, handler ordering and dispatch refusal retain a real HTTP/SQL
  fixture proof. The test explicitly registers `HandlePostSessions` at
  `POST /api/sessions`; it does not exercise the production launch-permissions
  wrapper. It is **not** claimed as a database-free 3a test. No server-wide fake
  repository was added. Existing positive model/effort and per-model-list tests
  remain unchanged.

Timing used three separate processes before and three after, on the same
configured dedicated database and shared host. Each invocation selected the
three packages together; unrelated host workloads were not isolated. Values
are top-level Go JSON `pass.Elapsed` seconds, including each test's cleanup,
excluding compilation/startup and package TestMain cleanup. After values sum
all replacement tests and retained new real proofs **within each sample**
before calculating statistics. Millisecond/centisecond rounding is inherited
from Go JSON; a reported 0.000 is below its resolution, not zero work.

| Selected group | Before samples (s) | After samples, including retained proofs (s) | Before median [min, max] | After median [min, max] | Observed median reduction |
| --- | --- | --- | --- | --- | --- |
| Retry / rank 1 | 35.070, 35.080, 35.050 | 0.040, 0.070, 0.050 | 35.070 [35.050, 35.080] | 0.050 [0.040, 0.070] | 35.020 s |
| Accounting / rank 2 | 39.510, 43.550, 43.300 | 1.350, 1.630, 1.440 | 43.300 [39.510, 43.550] | 1.440 [1.350, 1.630] | 41.860 s |
| Validation / rank 11 | 15.530, 15.540, 12.290 | 1.690, 2.100, 1.900 | 15.530 [12.290, 15.540] | 1.900 [1.690, 2.100] | 13.630 s |

Do not sum these independent medians as a measured wall-clock gate saving.
Database fixture consolidation and removal of real timer waits / mass filesystem
creation are included; their individual contributions were not separately
measured. Before samples each passed 3 top-level tests and 2 subtests; after
samples each passed 7 top-level tests and 2 subtests, with no skips or failures.
The validation table still contains 14 inputs, without subtest receipts.
Final regional lint reported 0 issues; vet, build, scrub and whitespace checks
passed. The server lint/vet/build checks were repeated after the loopback fixture
correction; daemon and accounting code had not changed after their checks.

Exact reproduction, from `runner`, with the user's configured dedicated test
DB in `TEST_DATABASE_URL`, `DATABASE_URL` equal to it, `GOWORK=off`, the prescribed
Go PATH and existing worker limits. Timing is the reason for `-count=1` here:

```sh
# Before: run this command three times at the baseline above.
go test ./internal/daemon ./internal/clusterworkspace ./internal/server -run '^(TestInterruptedWaiterRetriesUntilAcknowledged|TestClusterRefusedDeepMemberStillAccountsAggregate|TestPostSessionsRejectsBadModelOrEffort)$' -count=1 -json
# After: run this command three times at this change.
go test ./internal/daemon ./internal/clusterworkspace ./internal/server -run '^(TestInterruptedWaiterRetriesUntilAcknowledged|TestInterruptedWaiterCloseUsesRealHTTP|TestWaiterNoticeTimerCancellation|TestClusterRefusedDeepMemberStillAccountsAggregate|TestClusterDeepHardLinksAccountAggregateOnDisk|TestModelRulesRejectBadModelOrEffort|TestPostSessionsRejectsBadModelOrEffort)$' -count=1 -json
# Development verification used the same after selection without -count=1.
"$GOLANGCI_LINT" run --config ../.golangci.yml ./internal/daemon/... ./internal/clusterworkspace/...
"$GOLANGCI_LINT" run --allow-serial-runners --config ../.golangci.yml ./internal/server/...
go vet ./internal/daemon ./internal/clusterworkspace ./internal/server
go build ./internal/daemon ./internal/clusterworkspace ./internal/server
# Repository root:
./scripts/scrub.sh
git diff --check
```

TDD evidence: baseline assertions passed before refactoring. Temporary behavioral
mutations then made the new tests red: fixed 5/5/5-second waits failed the retry
schedule assertion; returning at the member depth refusal failed both byte and
entry aggregate cases; bypassing model-name validation failed the five malformed
model cases. All mutations were restored before the three green after samples.
No compile error is counted as behavioral red evidence.

Tooling limitations/failures: the first temporary Go mutation invocation rejected
its source/argument paths before changing code; compiling the Go helper resolved
that. One after-run launch used the wrong relative restoration path and was
interrupted (exit 130), excluded from measurements, then restarted after verified
restoration. The initial server lint attempt exited 3 because another lint
process held the global lock; the recorded command queues serially instead.
The first loopback HTTP run returned 404 because the chosen contract-route
registrar does not own this route. Explicit handler registration fixed the test
fixture; that failed attempt was excluded and all three final samples restarted.
All temporary tooling was Go. No Python or shell tools were added, no containers
or images were created, and **no `/tmp/cx/outside.sh` or ptrace tracer was used**.
Normal fixture/test cleanup removes owned databases and sparse-file directories;
temporary analysis programs, mutation backups and raw logs are removed after
recording results. No full suite, browser/performance gate, race run or independent
review was performed. Broader kernel and server behavior remains for the
coordinator's gate; these results establish only the selected tests. No open
product decision blocks this change.

## BLG-397 follow-up: remaining server candidates, batch 2 (0.6.0)

Baseline is main `3da026bf`. This experiment selects only the six requested
server tests; it makes no full-suite or gate timing claim. The repository's
`runner/skills/test-driven-development/SKILL.md` governs the proof layers and
behavioral mutation checks.

### P6 body investigation before optimization

The earlier fixture experiment did not change P6. This batch first added
opt-in `testsupport.Phase` spans inside its original 30-case table, separating
case-specific SQL setup, initial snapshot, authority-state change and delivery.
Three diagnostic samples found the following **summed within each sample**
seconds. These exploratory processes overlapped other work, including the
initial baseline run; they diagnose the wait, not the final speedup.

| P6 body span | Sample 1 | Sample 2 | Sample 3 |
| --- | ---: | ---: | ---: |
| Case SQL setup | 0.900 | 0.772 | 0.679 |
| Initial snapshot | 0.251 | 0.228 | 0.218 |
| Recipient/desktop/runtime changes | 0.162 | 0.154 | 0.156 |
| All deliveries | 5.668 | 5.600 | 5.584 |
| Polling SSE deliveries, included above | 5.213 | 5.167 | 5.192 |

`HandleRunnerEventStream` drains the fixture's seeded capabilities event,
then waits its default one-second poll after a nonempty page before rechecking
the terminal session. Five polling cases account for about five seconds of
the former six-second body. This is unrelated to fixture migration or a slow
result query. The table now uses the existing `ssePollInterval` pacing seam;
production pacing is unchanged. No new clock or repository API is needed.

### Proof retained and policy moved

- **Desktop attach timeout:** all existing/fresh/pending assertions still run
  through the real websocket handshake and SQL bind/fence. They check failed
  connection removal, advisory-lock release, reservation release, newly bound
  generation, owner/revision and disconnected status/reason. Existing enrollment
  still waits for the real five-second fence deadline. Fresh and pending
  enrollment use an explicitly advanced context at the policy hook, after
  binding, with `DeadlineExceeded` propagation through the actual fence. The
  only production seam is a nil-by-default, unexported attach-context function
  on the Hub. These assertions cannot become layer 2: SQL cancellation and
  enrollment/generation cleanup are their subject, rather than a pure policy
  decision. No real deadline proof was replaced with an immediate error return.
- **P6 member results:** `workspaceResultVisible` extracts the unchanged
  visibility/manifest owner/runtime/viewer predicate into a layer-2 table,
  including missing manifests and internal callers. SQL still supplies current
  ownership and runtime. All six endpoints × five authority states retain their
  original outcome-presence, no-private-metadata and shared-array-schema
  assertions. Each state owns one fixture and one pre-change snapshot; its six
  delivery subtests read that state. Fixtures fall from 30 to 5, without sharing
  a database between independent tests. Unknown and reassigned desktops and
  changed runtime are still written in SQL; callback and queued delivery still
  revalidate the captured snapshot. Builder, handler, SSE, callback serialization
  and SQL revalidation cannot be replaced by the predicate table.
- **Authoritative native ask:** a synchronous layer-2 table in the agent package
  checks that eight ordinary sources preserve the pending question, answer
  channel, text, source and client ID; only the matching authoritative answer
  enters the answer channel, leaving ordinary input queued. All 14 child/top-level
  server cases retain actual HTTP messaging, SQL question state, actual
  server-produced frames, the actual agent loop, person-answer labeling and eventual
  provider input. An exact frame receipt replaces the 300 ms `awaitAll` drain;
  provider call notifications replace polling sleeps. The 75 ms absence wait is
  replaced by the synchronous queue invariant and checking every received tool
  result/answer-source message against the actual person's answer. Source routing,
  persisted recipients and wire attribution remain integration assertions.
- **Private HTTP failures:** layer 2 directly checks bounded deadline setup,
  one write, short-write error conversion, no flush after write failure,
  flush-error propagation, complete JSON and no bytes on unsupported deadlines.
  All original two endpoints × nine modes remain at SQL/HTTP level, using one
  fixture per endpoint. Each request is cloned, and the explorer peer consumes
  the real broker's idempotent cancellation frames between RPCs. Every original
  lock-held-during-write/flush and lock-released-after-failure check remains.
  Four actual socket stalls still exercise real deadline unblocking. SQL locks,
  socket deadlines, route capability refusals and private-byte suppression cannot
  move to the direct writer table.
- **Resolve grant refusals:** requester and placement/configuration decisions
  have dependency-free layer-2 tables. `checkGrantTarget` retains its initial
  database-availability refusal, then calls the extracted placement predicate.
  The complete original refusal table retains status/message, nil grant and
  zero SQL session/grant rows after every case. It owns one private database;
  each case gets fresh API, Core-list fake, gateway and upstream peer state.
  Connection ownership still uses the supplied Core connection-list fake;
  live hashes, changed descriptions/defaults and upstream failures still use
  the real gateway client/HTTP protocol. They cannot be proved by a placement
  predicate. No provider-owned fake contract is claimed.
- **Delegation matching, rejected:** the prototype extracted direct/confinement/
  live-owner eligibility and ordered local-only rule selection into pure tables,
  then retained every original SQL/HTTP matrix assertion using a single private
  fixture and `TRUNCATE sessions, delegation_rules CASCADE` between cases. All
  three samples were slower: 27.400/27.210/28.500 s against baseline
  18.720/21.410/17.440 s (median 27.400 versus 18.720). The reset cost was not
  separately instrumented, so this is a measured regression of that prototype,
  not a claim about every possible database reset. All delegation code, tests
  and fixture changes were reverted. The original `TestTrustDirectAndOffered`
  already covers cheap policy directly. The remaining matrix also proves SQL
  account/engine/runtime/daemon candidate filtering, revoked rows, grant/rule
  precedence, default-runtime resolution, HTTP offered choices and spawn/no-spawn.
  Moving those meanings needs a repository contract, not another consumer map;
  no such design is introduced without a demonstrated gain. No delegation
  speedup is claimed.

### Development failures and TDD evidence

Baseline assertions passed before refactoring. The first integrated development
attempt failed five explorer delivery modes: the reusable fixture peer treated
the previous request's `cancel_workspace_explorer` as the next RPC. That made
the real request time out and the generic error response trigger the delivery
probes. The peer now explicitly consumes cancellation frames and rejects
unexpected frame types; all original assertions remain. The next integrated
attempt passed that entire delivery matrix but failed one added cluster policy
case: its synthetic parent lacked a desktop ID. The policy correctly refused;
fixing the test data made the pure table green. Neither failed attempt is a
timing sample or a product regression claim.

One pure-table verification also failed to build because the new test file was
renamed while vet was reading it. The verification was restarted after the
rename completed. This is tooling failure, not behavioral red evidence.

An additional desktop policy/welcome helper-path run was terminated by SIGTERM
(exit 143) after one subtest passed, with no test failure or explanation in its
output. It is incomplete evidence, not a flake or pass. The same helper paths
passed on rerun. Regional lint initially found one `staticcheck QF1003` issue in
the new visibility table; its `if/else` became a tagged `switch`. The visibility
table passed again with the helper-path rerun. This style-only test rewrite
followed the final timing samples; production code and assertions did not
change, so the timing experiment was not repeated for it.

Temporary mutations then bypassed the manifest visibility gate, local-only rule
filter, unsupported-engine refusal and short-write conversion, and sent ordinary
input into the ask's answer channel. The selected five top-level tests failed
behaviorally (20 failed subtests, 21 passed); no compile failure is counted as
red. The rule-selection probe belongs to the delegation prototype discussed
below; four retained tables accounted for 18 of those failed subtests. Every
mutation was restored before timing. The pure policy selection was green before
the mutations, and final after samples supply the restored green evidence.

The session has no readable/writable board: `ticket get 397` was refused and
`board access` confirmed propose-only access. A failure mark without a card was
also refused. Failure evidence is therefore retained here; no ticket changes
or review records are claimed.

### Measurement method and reproduction

Three repetitions run in each package process (`-count=3` is intentional for
timing). Top-level Go JSON `pass.Elapsed` includes each test's cleanup, but not
compilation, process startup or final TestMain template cleanup. Replacement
tables and retained proofs are summed within each sample before comparing
groups; all pure replacement tables report 0.000 s at Go JSON's resolution,
which does not mean zero work. The baseline and final timing processes do not
overlap this session's other experiments or quality checks. Unrelated host work
was active throughout; ranges reflect that shared environment. The earlier
overlapping exploratory baseline is excluded from the comparison. The rejected
delegation prototype was measured separately, before the final retained tree.

Commands below run from `runner`, with the prescribed Go PATH, `GOWORK=off`,
dedicated `TEST_DATABASE_URL`, matching `DATABASE_URL`, pinned `GOLANGCI_LINT`
and existing worker limits. No new shell or Python tooling was written. A
temporary Go JSON reader aggregated receipts and spans.

```sh
BATCH2_BASE_SELECTION='^(TestDesktopPersonalAttachPolicyTimeout|TestP6MemberResultsRecipientAndDelivery|TestAuthoritativeNativeAskQueuesEveryOrdinarySource|TestDelegationMatchMatrix|TestP6PrivateHTTPDeliveryFailures|TestResolveGrantRefusals)$'
# Original, unmodified main binary, retained from the initial baseline process:
go tool test2json -t -p github.com/blerglab/blerg-ai/runner/internal/server /tmp/batch2-baseline.test -test.v=test2json -test.timeout=10m -test.run="$BATCH2_BASE_SELECTION" -test.count=3
# Equivalent reproduction in an untouched baseline worktree:
go test ./internal/server -run "$BATCH2_BASE_SELECTION" -count=3 -json
# Body investigation, original P6 table with the opt-in spans:
BLERG_TEST_FIXTURE_TIMING=1 go test ./internal/server -run '^TestP6MemberResultsRecipientAndDelivery$' -count=3 -json
BATCH2_FINAL_SELECTION='^(TestDesktopPersonalAttachPolicyTimeout|TestP6MemberResultsRecipientAndDelivery|TestAuthoritativeNativeAskQueuesEveryOrdinarySource|TestResolveGrantRefusals|TestP6PrivateHTTPDeliveryFailures|TestGrantPlacementPolicy|TestGrantRequesterPolicy|TestWorkspaceResultVisibilityPolicy|TestFencedResponseDeliveryPolicy|TestOrdinaryInputQueuesBehindAsk)$'
go test ./internal/server ./internal/agent -run "$BATCH2_FINAL_SELECTION" -count=3 -json
# Additional touched shared-helper paths, no timing-cache override:
go test ./internal/server -run '^(TestDesktopPersonalAttachPolicyFailure|TestDesktopPersonalAttachWelcomeFailure)$' -json
# Successful helper rerun, also rechecking the lint-only visibility-table edit:
go test ./internal/server -run '^(TestDesktopPersonalAttachPolicyFailure|TestDesktopPersonalAttachWelcomeFailure|TestWorkspaceResultVisibilityPolicy)$' -json
"$GOLANGCI_LINT" run --allow-serial-runners --config ../.golangci.yml ./internal/server/... ./internal/agent/...
go vet ./internal/server ./internal/agent
go build ./internal/server ./internal/agent
# Repository root:
./scripts/scrub.sh
git diff --check
```

The discarded prototype's exact timing selection was the final selection plus
`TestDelegationMatchMatrix`, `TestDelegationPreapprovalPolicy` and
`TestDelegationRuleSelectionPolicy`, with the same `-count=3 -json` invocation.
It passed 39 top-level and 588 subtest receipts, without skips or failures;
passing did not qualify it for retention. Baseline passed 18 top-level and
399 subtest receipts, without skips or failures.

### Final three-sample results

All values are seconds, in sample order. After values include the group's new
pure tables and retained SQL/HTTP/socket proofs. The rejected delegation numbers
above describe the discarded prototype, not the final tree.

| Retained group | Before samples | Final after samples | Before median [min, max] | After median [min, max] | Median reduction |
| --- | --- | --- | --- | --- | --- |
| Desktop attach timeout | 16.380, 16.570, 16.600 | 6.270, 6.460, 6.410 | 16.570 [16.380, 16.600] | 6.410 [6.270, 6.460] | 10.160 |
| P6 member recipient/delivery | 19.460, 25.810, 22.220 | 2.610, 2.410, 2.580 | 22.220 [19.460, 25.810] | 2.580 [2.410, 2.610] | 19.640 |
| Authoritative native ask | 14.990, 12.670, 15.220 | 8.340, 8.930, 8.050 | 14.990 [12.670, 15.220] | 8.340 [8.050, 8.930] | 6.650 |
| P6 private HTTP failures | 11.110, 18.100, 12.200 | 3.810, 4.080, 4.160 | 12.200 [11.110, 18.100] | 4.080 [3.810, 4.160] | 8.120 |
| Resolve grant refusals | 9.530, 15.740, 15.230 | 1.520, 1.560, 1.780 | 15.230 [9.530, 15.740] | 1.560 [1.520, 1.780] | 13.670 |

Do not sum independent medians as a wall-clock gate saving. Fixture amortization
and wait removal are both included, and their isolated speedup contributions
were not measured. The retained after selection passed **30 top-level and 435
subtest receipts**, with no failures or skips. The five new layer-2 tables alone
account for 15 top-level and 120 subtest receipts across the three samples; the
remaining receipts exercise the retained real boundaries. Delegation remains
unchanged and is covered by its three baseline samples, not silently credited
as an optimization.

The additional helper/visibility rerun passed **3 top-level and 17 subtest
receipts**, without failures or skips. Thus retained timing and final focused
verification together have 33 top-level and 452 subtest pass receipts; the
interrupted helper attempt is excluded from that count.

Regional lint rerun reported **0 issues**; regional vet and build exited 0.
Scrub and whitespace checks passed. No full server suite, frontend, browser,
performance, race or independent-review gate was run. The coordinator retains
those broader checks. **No `/tmp/cx/outside.sh` invocation or ptrace tracer was
used.** No images or containers were created. Temporary Go analysis tooling,
baseline executable, mutation backups and raw measurement scratch files were
removed; ordinary completed fixture runs perform their normal cleanup. The
interrupted helper log is retained as the session artifact
`batch2-interrupted-helper.txt`.

Cleanup limitation: the SIGTERM run did not reach a final cleanup receipt, so
its marked fixture databases may await the normal abandoned-fixture sweeper.
Its run identity was not retained in the truncated log; no manually guessed
database removal or shortened global reaping age was used. This leaves an
operational cleanup uncertainty, not an open product decision.

## BLG-298 follow-up: rank 5 create deadline (0.6.0)

This bounded round starts at local main `2b0c3884`. The remaining highest-ranked
uncompleted candidate is `TestLaunchAmbiguousSecretCleanup` (rank 5). Ranks 1,
2, 3, 4, 6, 8, 10, 11 and 12 already have follow-ups above; rank 9's measured
prototype was rejected. No BLG-342 session end-to-end tests are changed.
Local scope estimate: size M, risk low, complexity medium. The supplied card
is the authority for this round; no board access, updates or failure marks
were attempted.

### Boundary and mutation proof

The production create deadline remains ten seconds. A nil-by-default,
per-`JobManager` context factory isolates that deadline from real HTTP/SQL work.
`TestLaunchCreateDeadlinePolicy` uses Go `testing/synctest` to prove the exact
ten-second default, a shorter parent deadline and an already canceled parent.
The expected ten seconds are literal in the test, independently of the
production constant. Waiting on `Done` advances virtual time, with no real
sleep or separate service. These are layer-2 policy proofs.

The original deadline mode waited in a client RoundTripper after its completed
HTTP exchange; it was not a socket-stall/unblocking proof. The ten-mode
SQL/HTTP cleanup matrix remains intact. Its deadline case advances a controlled context **after the real HTTP POST has committed**,
then returns that context's `DeadlineExceeded` through the real client failure
path. It reuses the explicit deadline context from the attach tests; no global
clock or deadline is changed. Every original assertion remains: two ownership
reads, exact UID deletion precondition, no duplicate allocation, runtime
generation, capability count, durable cleanup intent, no credential material
in persisted metadata, restart reconciliation, foreign/replaced refusal and
accepted/finalizer deletion handling. These SQL durability and HTTP protocol
proofs stay layer 3b. This removes the real ten-second timer wait from the
matrix; it does not replace real persistence or claim Kubernetes staging proof.

Both temporary mutations built and failed behaviorally:

- Changing the default factory to twenty seconds failed the default and
  canceled-parent deadline assertions (one top-level, two subtest failures).
- Detaching the parent with `context.WithoutCancel` failed the shorter-parent
  deadline and canceled-parent error/elapsed assertions (one top-level, two
  subtest failures).

The unaffected case passed in each mutation run. Both mutations were restored
before regional verification. An initial mutation command used a repository-root
path while running inside `runner`; its copy/edit failed, so the cached green
run that followed is excluded from mutation evidence. No behavioral red is
claimed for that command mistake. No new product failure or flake is claimed.

### Measurements and commands

Three repetitions were requested explicitly for timing (`-count=3`, never
`-count=1`). Terminal top-level Go JSON durations include test cleanup and
exclude compile/startup/TestMain final cleanup. The replacement policy table
is included in each after sample and reports 0.000 s at Go JSON resolution.
This is a focused selection measurement, not a whole-server or gate speedup.

All commands use the prescribed Go PATH, `GOWORK=off`, the dedicated test
Postgres URL and matching `DATABASE_URL`. The final before/after timing processes and regional verification use
`GOMAXPROCS=2 GOFLAGS=-p=2`; no frontend was touched or run. The final baseline
restored the original three changed source/test files from `2b0c3884` and
temporarily removed the new policy test, then restored the committed changed
files byte-for-byte before the final after run. The full package, baseline and
after timing processes ran sequentially; no other experiment or quality check
from this session overlapped their test execution. Unrelated host work remained
active; no host-load samples were captured, so non-timer cost is still noisy.

```sh
# From runner; baseline uses the unmodified main source:
go test ./internal/server -run '^TestLaunchAmbiguousSecretCleanup$' -count=3 -json
# Replacement policy plus every original SQL/HTTP case:
go test ./internal/server -run '^(TestLaunchAmbiguousSecretCleanup|TestLaunchCreateDeadlinePolicy)$' -count=3 -json
# Green policy test, then each temporary mutation, each without a cache override:
go test ./internal/server -run '^TestLaunchCreateDeadlinePolicy$' -json
# Restored full touched package:
go test ./internal/server -timeout 30m -json
"$GOLANGCI_LINT" run --concurrency 2 --allow-serial-runners --config ../.golangci.yml ./internal/server/...
go vet ./internal/server
go build ./internal/server
# From repository root:
./scripts/scrub.sh
git diff --check
```

| Selected group | Before samples (s) | After samples including policy table (s) | Before median (s) | After median (s) |
| --- | --- | --- | ---: | ---: |
| Ambiguous Secret cleanup | 18.460, 16.840, 19.270 | 10.270, 7.890, 7.250 | 18.460 | 7.890 |

The median reduction is **10.570 s** for this selected group. Baseline range:
16.840–19.270 s; after range: 7.250–10.270 s. Package-process elapsed was
55.175 s before and 25.738 s after for the three repetitions, including
TestMain cleanup. Neither aggregate is a whole-server-suite timing.

An initial exploratory comparison had before samples 18.780/14.390/14.380 s
and after samples 6.580/4.500/5.050 s. Its baseline inherited Go worker settings,
while after used two workers. Those samples are superseded by the matched-worker
comparison above and excluded from the final reduction claim.

The final selected baseline passed 3 top-level and 30 subtest receipts; the selected
after run passed 6 top-level and 39 subtest receipts. Neither skipped or failed.
The separate initial green policy run passed 1 top-level and 3 subtests.

Rank 7 remains a real 100,000-file/Git contention proof. Its filesystem and
resource assertions cannot be replaced by an in-memory list without losing
the measured boundary; it is unchanged. No additional server repository fake,
gate scheduling change or full-gate speed claim is introduced.

### Regional verification and limits

The restored full `internal/server` package passed in **1,739.702 s
(28 min 59.702 s)**, with **2,177 top-level passes, 1,892 subtest passes,
22 top-level skips and no failures**. The skips are the existing opt-in
acceptance, production-stack/recovery, browser HTTP fixtures, membership real-wire
and endpoint-measurement tests; they are not counted as passes. The run used
real configured Postgres. This full-package observation is substantially longer
than the historical audit trace; it is a different tree on a shared host with
two Go workers, not a controlled before/after full-package comparison. The
15-minute full pre-merge target is not established by this work.

Regional lint reported **0 issues**. Regional vet, build, scrub and whitespace
checks passed. No frontend, browser, performance, race, whole-repository or
release gate was run. No new route or UI was added. No independent review is
claimed. No `/tmp/cx/outside.sh` invocation or ptrace tracer was used; no images
or containers were created. Temporary Go summary programs, source snapshots,
mutation backups and measurement logs were removed after recording the counts.
Shared Go caches were left intact. Completed fixture runs performed their normal
cleanup. There are no open product decisions for this bounded move; rank 7 and
the rejected rank 9 design remain future work.

## BLG-298 continuation from `d4d87462`: ranked server moves

Each retained move below has its own commit and selected-test measurements.
Only moved tests run during each move; one full server package run follows the
last move. No frontend, browser, performance or ptrace suite is added to a round.
All Go commands run from `runner` with the prescribed PATH and dedicated
Postgres settings, `GOWORK=off GOMAXPROCS=2 GOFLAGS=-p=2`, and matching
`DATABASE_URL`/`TEST_DATABASE_URL`. Three timing repetitions use `-count=3 -json`;
mutation runs use `-json` without a cache override. Terminal top-level elapsed
includes test cleanup, excluding compilation/startup/final TestMain cleanup.
Replacement policy tests and retained boundary tests are summed within each
sample; 0.000 s is Go JSON resolution. Tests run sequentially within this session;
other host work is not isolated. No full-gate saving is inferred from these rows.

### Rank 6: move phrase routing outcomes to the policy boundary

The first pass already tested the 21 phrases directly but repeated every phrase
through SQL/HTTP. `postMessageRequest.requiresPerson` now owns the unchanged
kind/flags/coordinator/phrase predicate. Every original phrase checks both its
classifier and that routing predicate. Eight extra cases cover top-level asks,
explicit person requests, approval/no-wait combinations, and note/update kinds.
Two real authenticated HTTP requests retain status 201, recipient and persisted
`person_required` checks for both routing outcomes, including Unicode JSON input.

This moves the per-phrase routing vocabulary to layer 2, retaining the actual
HTTP decode, session coordinator lookup, persistence and recipient plumbing at
3b. The production caller uses the same request object the decoder filled; no
repository fake, global clock, shared cross-test fixture or request stub is added.

| Group | Before samples (s) | After samples including retained HTTP proof (s) | Median before → after (s) |
| --- | --- | --- | --- |
| Phrase classifier/routing + SQL/HTTP | 3.760, 2.090, 2.460 | 2.490, 0.490, 0.510 | 2.460 → 0.510 |

Before: 6 top-level and 63 subtest pass receipts across three samples. After:
9 top-level and 87 subtest pass receipts, no failures/skips. Each after sample
has all 21 original phrase cases plus eight new routing cases; the HTTP proof
uses two requests instead of 21 in one private database per sample.

Mutation evidence: inverting the classifier failed all 21 phrase subtests, the
HTTP test and the no-wait-worker routing case (3 top-level/22 subtest failures;
seven unaffected routing subtests passed). Forcing the real handler's decision
to false failed the retained Unicode-destructive HTTP recipient assertion
(1 top-level failure). Independently inverting only the new routing predicate
failed all 21 moved phrase-routing assertions plus six flag cases (2 top-level
and 27 subtest failures; the two non-ask cases passed). All mutations are
restored before committing. The restored selection passed 3 top-level and
29 subtests without failures/skips.

```sh
# Before:
go test ./internal/server -run '^TestQuestionDestructivePhrases(Only|HTTPPostgres)$' -count=3 -json
# After (including the new routing table):
go test ./internal/server -run '^Test(QuestionDestructivePhrases(Only|HTTPPostgres)|MessageRequiresPersonPolicy)$' -count=3 -json
# Classifier mutation uses the after selection without -count=3.
# Handler-bypass mutation:
go test ./internal/server -run '^TestQuestionDestructivePhrasesHTTPPostgres$' -json
# Pure routing mutation:
go test ./internal/server -run '^Test(QuestionDestructivePhrasesOnly|MessageRequiresPersonPolicy)$' -json
```

### Rank 8: amortize the retained authoritative-ask boundary fixtures

The existing layer-2 agent queue invariant is unchanged. Removing more of the
14 real-source integration cases would lose server routing, SQL question state,
actual daemon frames, person-answer labels and eventual provider input proof.
Instead, each child/top-level topology now owns one private database and each
of its seven source subtests creates a fresh session, loop, listener, provider
and question. Nothing is truncated or reset across cases. The fixture's test
handle is copied for each subtest so helper failures belong to the active case.
All original assertions and all 14 source/topology combinations remain. The
new two topology groups close their fixtures before the next group starts;
no database is shared across independent top-level tests.

This is fixture amortization around the already-moved policy, not a claim that
SQL durability became a lower-layer fake. Fixtures fall from 14 to 2 per sample;
all actual messaging paths and per-case session isolation remain.

| Group | Before samples (s) | Final restored after samples (s) | Median before → after (s) |
| --- | --- | --- | --- |
| Authoritative native ask, all sources/topologies | 10.220, 7.160, 8.630 | 4.400, 2.580, 2.780 | 8.630 → 2.780 |

Before: 3 top-level/42 subtest pass receipts. Final after: 3 top-level/48 subtest
pass receipts (42 original leaf cases plus six new grouping receipts), with no
failures/skips. The preliminary after samples were 4.810/3.130/3.610 s, also green;
the final row is the restored green run after mutation.

Temporary mutation in `agent.Loop.EnqueueInput` sent ordinary input into the
pending ask's answer channel while still queuing it. **Every one of the 14 leaf
cases failed** its person-answer/tool-result assertions; the two groups and root
also failed (1 top-level/16 subtest failures, no pass/skip receipts). The mutation
was removed byte-for-byte; no agent implementation change is retained. The
three final green samples use the original agent implementation.

```sh
# Before, after, and restored timing (same selection):
go test ./internal/server -run '^TestAuthoritativeNativeAskQueuesEveryOrdinarySource$' -count=3 -json
# Mutation, same selection without the repetition override:
go test ./internal/server -run '^TestAuthoritativeNativeAskQueuesEveryOrdinarySource$' -json
```

### Ranks 9, 10 and 11: retain the remaining boundary proofs

No tests were removed, changed or rerun for these skips. No new timing or mutation
result is claimed for an unchanged rank; their earlier measurements and mutation
proofs remain in the preceding follow-ups.

- **Rank 9, delegation matching:** `TestTrustDirectAndOffered` already covers the
  cheap direct-origin and offered-choice policy. The remaining matrix additionally
  proves real SQL account/engine/runtime/daemon filtering, revoked rules/grants,
  rule-versus-grant precedence, runtime resolution and HTTP spawn/no-spawn. A
  consumer-supplied candidate list cannot catch a broken SQL filter or revoked-row
  query. Replacing those cases with an inventory stub would lose that coverage.
  The earlier real-fixture reset prototype also regressed median time from
  18.720 to 27.400 s and was rejected. No new repository contract or replacement
  SQL conformance proof is introduced in this bounded round; the matrix stays.
- **Rank 10, private HTTP delivery:** the five scripted writer outcomes already
  have the layer-2 `TestFencedResponseDeliveryPolicy` table. The remaining two
  endpoint × nine-mode matrix probes the *real SQL advisory lock while writing
  and flushing*, verifies release after each failure and private-byte suppression,
  and includes four actual `net.Pipe` deadline stalls. Removing those checks in
  favor of another writer fake would lose route/fence lifetime or kernel deadline
  coverage. Advancing a fake clock cannot unblock a real socket deadline. The
  real stalls and SQL authority proofs stay unchanged.
- **Rank 11, model/effort validation:** all 14 invalid inputs already call
  `modelRuleProblem` at layer 2. The remaining single authenticated loopback
  HTTP/SQL rejection checks 422 and absence of a daemon spawn through the actual
  handler. Moving that last test to the validator would lose decoding/auth/route
  ordering and dispatch-refusal proof. There is no repeated policy matrix left
  in this HTTP test to move safely.

### Rank 12: keep grant refusal vocabulary at its existing pure seams

Four requester/login and nine placement/configuration refusals now use only the
existing `checkGrantRequester` / `checkGrantTarget` policy tables, rather than
rebuilding gateway/SQL fixtures for the same in-memory inputs. No SQL-loaded
ownership, revocation, live hash, changed description/default or transport case
moves: all remain in `TestResolveGrantRefusals`. A new direct `selectedTools`
table moves the `off`, unknown-mode and present-empty-tools vocabulary to layer
2, also covering missing hash, missing defaults and valid allow/propose/default
paths. The requester table adds the missing-account boundary.

The real early-refusal test keeps its agent, login-less-human and bare-host
requests, adding nil-grant and zero session/grant-row checks to the existing
zero-Core/upstream-call checks. The real refusal matrix retains missing-hash
and missing-default selector failures (400 and 422), checking shared resolver
propagation as well as every remaining gateway/storage outcome. Thus 16
repeated policy cases leave the 33-case service matrix, leaving 17 real cases;
their predicates, error status/vocabulary and nil selector results remain at
the pure seams. The availability-of-database refusal and actual gateway/SQL
implementation are unchanged. No production code or new fake is retained.

| Complete selected group | Before samples (s) | Final restored after samples (s) | Median before → after (s) |
| --- | --- | --- | --- |
| Refusals + early boundary + direct policies | 3.860, 1.820, 1.770 | 2.730, 1.380, 1.390 | 1.820 → 1.390 |

Each sample sums its real refusal matrix, early-refusal proof and all selected
pure tables before computing the median. The median reduction is 0.430 s;
shared-host noise limits precision. Before: 12 top-level/147 subtest passes.
Final after: 15 top-level/126 subtest passes, no failures/skips. Receipt counts
fall because the requester/placement cases were already duplicated at both
layers; no distinct policy input is removed. Preliminary green after samples
were 3.050/1.410/1.260 s; final values follow the restored mutations.

Mutation proof, with both database environment variables unset for pure tests:
reversing the requester-kind check, returning nil for every placement refusal,
and disabling selector mode/empty checks produced **3 top-level and 19 subtest
failures, six subtest passes, no skips**. All 16 moved cases failed their direct
policy assertions; the other failures cover valid-human, missing-account and
missing-default boundaries. A separate mutation bypassed the resolver's actual
requester and placement guard calls: the retained early-refusal test failed
because three Core calls and one upstream call escaped (one top-level failure).
All temporary production edits were restored byte-for-byte before final timing.

```sh
# Before:
go test ./internal/server -run '^Test(ResolveGrant(Refusals|EarlyRefusalsMakeNoCalls)|GrantPlacementPolicy|GrantRequesterPolicy)$' -count=3 -json
# After / restored:
go test ./internal/server -run '^Test(ResolveGrant(Refusals|EarlyRefusalsMakeNoCalls)|Grant(PlacementPolicy|RequesterPolicy|SelectedToolsPolicy))$' -count=3 -json
# Combined independent direct-policy mutations:
env -u TEST_DATABASE_URL -u DATABASE_URL go test ./internal/server -run '^TestGrant(PlacementPolicy|RequesterPolicy|SelectedToolsPolicy)$' -json
# Guard-call bypass mutation, real configured SQL/gateway fixture:
go test ./internal/server -run '^TestResolveGrantEarlyRefusalsMakeNoCalls$' -json
```

### Continuation final regional checks

The retained code head for the final checks is `68778038`. Regional lint reports
**0 issues**; vet, build, scrub and whitespace checks pass. The final whole-server
invocation is run once, after every move and all quality checks. Its 45-minute
package failure guard is declared before starting because the preceding round
needed 28 min 59.702 s on this shared host. No individual test deadline or timing
assertion is weakened. Before/after selection measurements use the same two-worker
settings; whole-package observations across rounds are not controlled comparisons.

```sh
# runner; prescribed Go/DB environment and GOMAXPROCS=2 GOFLAGS=-p=2:
"$GOLANGCI_LINT" run --concurrency 2 --allow-serial-runners --config ../.golangci.yml ./internal/server/...
go vet ./internal/server
go build ./internal/server
go test ./internal/server -timeout 45m -json
# Repository root:
./scripts/scrub.sh
git diff --check
```

That single final server invocation passed in **938.597 s (15 min 38.597 s)**:
**2,179 top-level passes, 1,895 subtest passes, 22 top-level skips, no failures**.
The same existing acceptance, real-engine/production-stack, browser-fixture,
membership real-wire and endpoint-measurement opt-ins skipped; none counts as a
pass. All moved selections had zero skips. The earlier 1,739.702 s full-package
observation used a different tree and shared-host load; the much shorter current
package observation must not be attributed to these three small moves. The
15-minute *full pre-merge matrix* target is still not established by this run.

No full frontend/browser/performance, race, whole-repository, release or
independent-review gate ran. No session end-to-end or worker-question changes
from another worker were edited. Local main remained `4c87744e` through final
verification, with no overlapping changes in the edited sources since the common
base; no conflict required pulling/merging main in this round. There were no
unexpected test failures; the failed executions above are deliberate behavioral
mutations, all restored and followed by green verification.

No `/tmp/cx/outside.sh` or ptrace tracer was used. No images, containers or new
permanent tooling were created. Temporary Go summary tooling, mutation/source
backups and raw timing/verification logs were removed after recording results;
normal completed fixture runs perform their own cleanup. Shared Go caches remain
intact. There are no open product questions for this bounded continuation. The
three code moves are separate commits; the skipped-rank rationale and final
verification are documentation commits. No push was performed.

## BLG-298 remaining ranks from `8646e7d9`

This round considers ranks 1, 2, 3, 4 and 7 on the retained tree. Historical
35/34-second labels are not current timings: ranks 1 and 2 already have the
first major rewrites documented above. Only moved tests run within each move;
each package whose code/tests change gets one whole-package run at the end.
All Go commands use the prescribed Go PATH/database settings and
`GOWORK=off GOMAXPROCS=2 GOFLAGS=-p=2`. Timing repeats use `-count=3 -json`, never
`-count=1`; mutations omit the repetition override. Top-level JSON elapsed
includes test cleanup, not compile/startup/final TestMain cleanup. New pure
proofs and retained boundary proofs are summed within each sample. Go JSON
0.000 s is resolution, not zero cost. Shared-host work remains uncontrolled.
No sum of group medians is a full-gate wall-clock saving.

### Ranks 1 and 2: already moved; preserve the remaining real proofs

- **Rank 1:** `TestInterruptedWaiterRetriesUntilAcknowledged` already injects its
  wait scheduler and checks exactly 5/10/20 seconds, four attempts, durable
  notice retention until acknowledgement and final removal. Its previous
  measured median is 0.050 s, not the historical 35.010 s. The separate real
  HTTP close, timer adapter, file replay and restart proofs remain. Removing
  these in favor of another in-memory retry table would lose HTTP cancellation,
  durable-file or restart delivery coverage. No further move is retained.
- **Rank 2:** the 500,001-entry/11-GiB policy inputs already use a bounded fake
  directory stream. It still traverses beyond the member-depth refusal and
  requires aggregate exhaustion to block both members. Replacing that traversal
  with a precomputed counter would miss the original early-return bug. The
  small real 130-directory sparse-file/hard-link proof retains actual opens,
  depth refusal, stat and two logical names for one inode. Shrinking away its
  depth/hard-link boundary would lose OS proof. No further move is retained.

No new timing/mutation result or package test execution is claimed for either
unchanged rank. Their earlier green measurements and behavioral mutation proof
remain above. `clusterworkspace` is therefore not a touched package this round.

### Rank 3: move the remaining fence timer policy to virtual time

`authorityFenceContext` owns the unchanged five-second default, shorter-parent
inheritance and parent cancellation policy. Its three `testing/synctest` cases
also require runtime-authority values to survive. Literal expected five/one/zero
seconds are independent of the production timeout variable. The existing real
pool-exhaustion timeout/retry test remains unchanged; real PostgreSQL timeout
and acquisition behavior is not replaced by a context stub.

All three existing/fresh/pending attach cases now use the existing per-Hub
explicit-expiry seam, advanced only after the actual SQL bind reaches the hello
hook. They retain actual websocket refusal, new-generation/owner/revision binding,
advisory-lock and reservation release, hub removal and disconnected status/reason.
The old five-second wait was a callback waiting on `ctx.Done`, not a socket-stall
proof. Every callback also checks that the actual fence supplied a deadline no
more than five seconds away, independently of the new direct policy test.

| Group, including new policy proof | Before samples (s) | Final restored after samples (s) | Median before → after (s) |
| --- | --- | --- | --- |
| Desktop attach deadline, all three enrollment states | 7.870, 6.130, 6.130 | 2.840, 1.160, 1.130 | 6.130 → 1.160 |

Before: 3 top-level/9 subtest pass receipts. Final after: 6 top-level/18 subtest
pass receipts (three database policy and three server integration repetitions),
no failures/skips. New policy durations report 0.000 s and are included above.
Earlier green after samples before the extra wiring check were
2.360/0.970/0.950 s; those are superseded by the final row.

Mutations: a ten-second default failed default and canceled-parent policy cases;
detaching the parent failed shorter-parent and canceled-parent cases. Each run
had one top-level/two subtest failures and one subtest pass, with database
variables unset. A third mutation bypassed the helper in the actual SQL fence,
using ten seconds at its call site: **all three attach enrollment states failed**
the new bounded-context assertion (one top-level/three subtest failures). Thus
an unused, correct helper cannot hide a wrong production timer. All mutations
were restored before the final samples. No compile failure is counted as red.

```sh
# runner; before:
go test ./internal/server -run '^TestDesktopPersonalAttachPolicyTimeout$' -count=3 -json
# after/restored (all new policy and retained integration proof):
go test ./internal/db ./internal/server -run '^(TestAuthorityFenceDeadlinePolicy|TestDesktopPersonalAttachPolicyTimeout)$' -count=3 -json
# Each direct policy mutation:
env -u TEST_DATABASE_URL -u DATABASE_URL go test ./internal/db -run '^TestAuthorityFenceDeadlinePolicy$' -json
# SQL fence helper-bypass mutation, with configured database:
go test ./internal/server -run '^TestDesktopPersonalAttachPolicyTimeout$' -json
```

### Rank 4: retain every SQL/serialization case with one owned database

The first rewrite already moved visibility policy down and reduced 30 fixtures
to five. This follow-up retains **all five authority states × six delivery paths**,
but the matrix owns one private database. Each state gets a fresh daemon UUID,
session rows, API, Hub, signing key and captured snapshot through
`newPrivFixtureWithPool`. No state is reset or truncated; unknown owner,
reassignment and runtime change still update actual, case-specific SQL rows.
Ordinary privacy tests retain `newPrivFixture` and its original private-database
allocation and default daemon ID. No production interface or fake is introduced.

Every original metadata-presence/suppression and shared-array-schema assertion
remains. Callback and queued delivery still revalidate the pre-change snapshot;
builders, HTTP, both SSE paths, ownership and runtime checks remain real.
This is fixture amortization around already-moved policy, not a lower-layer
replacement for SQL authority or serialization.

| Group | Before samples (s) | Final restored after samples (s) | Median before → after (s) |
| --- | --- | --- | --- |
| P6 member recipient/delivery, all 30 combinations | 3.000, 1.670, 1.640 | 1.780, 0.850, 0.750 | 1.670 → 0.850 |

Both selections passed 3 top-level/105 subtest receipts with no skips/failures:
90 leaf combinations plus 15 state grouping receipts. Preliminary green after
samples were 2.050/0.730/0.720 s; final values follow mutation restoration.

Mutation proof covers every leaf: allowing results despite visibility and
bypassing captured-payload revalidation failed **all 24 negative deliveries**
(foreign/unknown/reassigned/runtime-changed), with owner deliveries passing.
That run had one top-level/28 subtest failures and seven subtest passes, including
state grouping receipts. A separate mutation withholding all owner outcomes
failed **all six owner deliveries** (one top-level/seven subtest failures;
28 subtest passes). Both temporary production changes were restored before the
final three green samples. No private-data or delivery boundary was dropped.

```sh
# runner; before, after and restored timing:
go test ./internal/server -run '^TestP6MemberResultsRecipientAndDelivery$' -count=3 -json
# Each visibility/revalidation or owner-outcome mutation:
go test ./internal/server -run '^TestP6MemberResultsRecipientAndDelivery$' -json
```

### Rank 7: keep the real large index, remove redundant fixture directory work

The test still creates and commits 100,000 real source files across 100 directories,
plus the match and ignore files, and creates 250 ignored build files. Each directory
is now created once instead of through the generic helper for every file: 99,900
redundant directory preparations disappear. A new NUL-delimited real-index count
requires exactly 100,002 entries. Search, real Git, contention delays, ignore rules,
unrelated-path exclusion and the existing preparation budget are unchanged.
Replacing the index with a fake would lose the physical-scale proof and remains
rejected. This is fixture work reduction, not a claim that that boundary moved.

| Group | Before samples (s) | Final restored after samples (s) | Median before → after (s) |
| --- | --- | --- | --- |
| Real large-index search under contention | 8.400, 8.240, 8.290 | 8.260, 8.190, 8.170 | 8.290 → 8.190 |

Each timing selection passed three top-level tests, no subtests/skips/failures.
The 0.100 s median improvement is small, with overlapping sample ranges; do not
attribute a reliable suite speedup to this host measurement. The retained change
reduces known fixture operations and strengthens the scale invariant. Preliminary
after samples were 8.530/8.150/8.020 s and are superseded above.

Removing `--ignored` from the actual Git query failed the retained exclusion
assertion with 100,103 unrelated paths. Shrinking the source fixture to 99
directories failed the new real-index count with 99,002 entries. Both were
behavioral failures (one top-level failure each), restored before final timing.
All focused runs were inside the session; none needed a ptrace launcher.

```sh
# runner; before/after/restored timing:
go test ./internal/daemon -run '^TestWorkspaceSearchLargeIndexUnderContention$' -count=3 -json
# Each temporary mutation:
go test ./internal/daemon -run '^TestWorkspaceSearchLargeIndexUnderContention$' -json
```

### Final regional verification for ranks 1/2/3/4/7

Only `db`, `server` and `daemon` changed; `clusterworkspace` was inspected but
left unchanged. Local main was checked for overlapping edits in the changed
source files before final verification; none required bringing in session-suite
or worker-question changes. BLG-342/session end-to-end sources were not edited.
All moved tests ran separately during each move. After the last move, each
whole touched package ran once, sequentially, without `-count=1`.

The one outside-session invocation below contains daemon tests that own a
ptrace tracer. It is the **only** `/tmp/cx/outside.sh` use in this round. Focused
large-index tests, database and server suites, lint, vet and build ran in the
session. Docker/live-engine/helper/staging opt-ins were not enabled, including
`TestSessionEndToEndProcessShapes`; skipped proof is not claimed as passing.
No frontend, browser, race, other Go module or full integrated gate was run.

Exact final commands, from `runner` unless noted:

```sh
export PATH=$HOME/go/bin:$HOME/.local/go/bin:$PATH
export GOWORK=off GOMAXPROCS=2 GOFLAGS=-p=2
export TEST_DATABASE_URL=postgres://postgres:pw@127.0.0.1:55440/postgres
export DATABASE_URL=$TEST_DATABASE_URL
export GOLANGCI_LINT=$HOME/.cache/blerg-tools/v2.13.1/golangci-lint
"$GOLANGCI_LINT" run --concurrency 2 --allow-serial-runners --config ../.golangci.yml ./internal/db/... ./internal/server/... ./internal/daemon/...
go vet ./internal/db ./internal/server ./internal/daemon
go build ./internal/db ./internal/server ./internal/daemon
go test ./internal/db -timeout 30m -json
/tmp/cx/outside.sh "$PWD" env GOMAXPROCS=2 GOFLAGS=-p=2 DATABASE_URL="$TEST_DATABASE_URL" go test ./internal/daemon -timeout 30m -json
go test ./internal/server -timeout 45m -json
# Repository root:
bash scripts/scrub.sh
git diff 8646e7d9..HEAD --check
```

Lint reported zero issues; vet, build, scrub and whitespace checks passed.
An initial attempt to run the disposable timing summarizer without the specified
Go PATH/GOWORK failed reading the workspace version; it did not run tests. The
same summarizer succeeded with the prescribed Go environment. Expected mutation
failures are documented per move; they are not final-suite failures.

| Whole touched package, one final run | Elapsed (s) | Top-level pass / skip | Subtest pass / skip | Failures |
| --- | ---: | ---: | ---: | ---: |
| `internal/db` | 74.896 | 272 / 0 | 81 / 0 | 0 |
| `internal/daemon` | 246.278 | 1564 / 75 | 1039 / 25 | 0 |
| `internal/server` | 1012.006 | 2179 / 22 | 1895 / 0 | 0 |

The server total is 16m52.006s on this shared host; the preceding round was
938.597s. These are separate full-package observations, not an isolated
before/after speed claim. Focused three-sample comparisons above are the move
measurements. The 15-minute integrated target remains unproven.

Database had no skips. Daemon skips include live-engine/credentialed fixtures,
Docker/sandbox/quota/VM/host-helper opt-ins, the gated session end-to-end suite,
large-history/benchmark opt-ins, mount privileges and an external checkout.
Server's 22 existing skips are unchanged from the preceding regional run.
All final package runs passed on their first attempt; no final-suite reruns.

Disposable timing summarizer, source mutation backups, timing/mutation JSON,
final package/quality logs and the three files made by this round's single
outside-session launcher were removed after recording results. Test-owned Git
fixture directories cleaned up through their existing `t.TempDir` lifecycle.
No images or containers were created manually. No pushes. No open questions.
