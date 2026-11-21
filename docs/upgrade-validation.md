# Desktop upgrade and release gate validation

This is implementation evidence for BLG-171 and BLG-322, not release acceptance.

## Delivered

- Desktop `--upgrade [vX.Y.Z]`: published stable selection, isolated Git refs,
  local-change and active-work refusal, exclusive upgrade lock, consistent
  three-database backups with matching settings/credentials, detached installation,
  exact service/build verification, and private persisted recovery metadata.
- Release gate: required Markdown upgrade notes, RC publication, exact-SHA and
  branch/event workflow checks, Linux/macOS security jobs, atomic tag/main push
  with a main lease, GitHub release creation last, immutable-tag refusal, saved
  public commit identity, and accepted unchanged RC-tree promotion.
- Existing private-history filtering, ancestry checks and scrub remain enforced.
- Desktop documentation replaces the stale workspace gating section.

## Failing-first evidence

New package tests first failed because the implementation did not exist. A later
behavioral regression test demonstrated that stale daemon connection state could
incorrectly report success; it failed before the fresh-PID check was implemented.
Version-verification tests likewise preceded their implementation.

## Final checks

Commands run from `contracts`, with the pinned Go toolchain on PATH:

```sh
GOWORK=off go test -race -json ./desktopupgrade ./releasegate ./cmd/blerg-upgrade ./cmd/blerg-release
GOWORK=off go build -o /tmp/blerg-upgrade-check ./cmd/blerg-upgrade
GOWORK=off go build -o /tmp/blerg-release-check ./cmd/blerg-release
GOWORK=off go vet ./desktopupgrade ./releasegate ./cmd/blerg-upgrade ./cmd/blerg-release
GOWORK=off "$HOME/.cache/blerg-tools/v2.13.1/golangci-lint" run --allow-parallel-runners ./desktopupgrade/... ./cmd/blerg-upgrade/... ./releasegate/... ./cmd/blerg-release/...
```

Results: 11 top-level tests passed (31 including subtests), zero test failures.
Both library packages passed with the race detector. Both commands compiled;
the command packages have no standalone Go tests. Build and vet passed. Pinned
lint reported zero issues. Parallel-runner mode gives this invocation its own lint
lock; it does not increase test or build worker limits.

From the repository root:

```sh
GO="$HOME/go/bin/go" bash scripts/release-public_test.sh
bash install/desktop/hostcheck_test.sh
BLERG_SKIP_KUBECTL=1 bash install/desktop/compose_test.sh
bash -n install/desktop/blerg-up.sh install/desktop/upgrade.sh scripts/release-public.sh
bash scripts/scrub.sh
git diff --check
```

Release/export compatibility: 13 named checks passed against temporary remotes.
Hostcheck and Compose checks each passed their script gate. Shell syntax, scrub,
and diff whitespace checks passed. No frontend changed, so frontend typechecking
and browser suites were not run. No full browser or performance suite was run.

Initial validation attempts also exposed environment/setup issues: a root Go test
with `GOWORK=off` lacked a module, the default PATH selected an older Go for lint,
and lint's shared lock was already held. Correct module directories, the pinned
Go PATH, and a separate lint lock resolved these. The first shell fixture run
failed cleanup after its isolated HOME caused a toolchain download into a
read-only module cache; retaining the existing Go caches fixed the fixture.

## Remaining work and limits

- The requested admin UI, release-notes confirmation, live authorization checks,
  independent host-service worker, progress API and browser reconnect flow are
  not implemented. The existing release notice remains unchanged.
- No whole-stack previous-release-to-candidate installation or restore drill was
  run on Linux, macOS or Kubernetes. The review round below adds a real local
  database and credential restore drill; application installation and daemon
  replacement remain simulated. This is not release acceptance.
- Cluster stays command-only. No controller or new cluster permissions were added.
- Automatic publication recovery after an immutable tag lands but GitHub release
  creation fails is not implemented; CONTRIBUTING documents manual recovery.
- The release API tests do not yet cover every adversarial/pagination boundary in
  the design. Candidate acceptance is an operator attestation with evidence
  references, not an automated upgrade drill or an independent owner signature.
- #298 and BLG-319 prerequisites were not independently audited here.
- No public remote was pushed, tagged, or released. Test refs existed only in
  temporary local bare repositories. The prior design branch was merged into
  this work branch; main and other working branches were not changed.

## Review fixes and local drill

The review round adds regression checks for dry-run ref pollution, tag-only RC
publication, nested upgrade notes and fenced shell comments, every nonterminal
session status, truncated dumps, pre-migration restart, ancestry refusal, runner
commit verification, private install logs, and bounded complete-backup retention.

Run the disposable database drill from the repository root:

```sh
BLERG_UPGRADE_DRILL=1 GOWORK=off go -C contracts test -v ./desktopupgrade -run 'TestUpgradeWithRealGitAndFakeServices/drill|TestDumpTrailer'
```

It requires Docker Compose and the local `postgres:16` image. A unique Compose
project uses tmpfs storage and is removed at test cleanup. It upgrades schemas
and data in all three databases, validates real dump trailers (including patched
PostgreSQL restriction terminators), restores the dumps, checks old schemas and
data, and restores `desktop-credential.json`. Git checkout and backup orchestration
are real; the application installer and host daemon are simulated. This is a
passing local database/credential recovery drill, **not** whole-desktop, macOS,
Kubernetes, or owner release acceptance.

The release shell fixture now invokes `--push` through the actual CLI, CI gate,
and pre-push hook against temporary local bare remotes. Only GitHub responses and
GitHub remote-identity discovery are faked. It publishes tag-only RCs and stable
promotions, checking stable ancestry and preservation of public contributions.
No public remote is contacted.

### Final review-round checks

Commands below ran from the repository root unless a module directory is named.
Test counts include subtests; no failures or skips in the selected final Go runs.

```sh
GOWORK=off "$HOME/go/bin/go" -C contracts test -race -json ./desktopupgrade ./releasegate ./agentsmanifest ./cmd/blerg-upgrade ./cmd/blerg-release
# 66 tests/subtests passed (32 top-level).
GOWORK=off TEST_DATABASE_URL="$TEST_DATABASE_URL" "$HOME/go/bin/go" -C runner test -race -json ./internal/server -run 'TestRunnerManifest|TestAgents|TestOpenAPI|TestPackages'
# 11 tests/subtests passed (9 top-level).
BLERG_UPGRADE_DRILL=1 GOWORK=off "$HOME/go/bin/go" -C contracts test -v ./desktopupgrade -run 'TestUpgradeWithRealGitAndFakeServices/drill|TestDumpTrailer'
# 3 tests/subtests passed (2 top-level); real PostgreSQL restore drill.
GO="$HOME/go/bin/go" bash scripts/release-public_test.sh
# 13 named checks passed; real gate and hook, fake remotes only.

GOWORK=off "$HOME/go/bin/go" -C contracts build ./desktopupgrade ./releasegate ./agentsmanifest ./cmd/blerg-upgrade ./cmd/blerg-release
GOWORK=off "$HOME/go/bin/go" -C contracts vet ./desktopupgrade ./releasegate ./agentsmanifest ./cmd/blerg-upgrade ./cmd/blerg-release
GOWORK=off "$HOME/go/bin/go" -C runner build ./cmd/server
GOWORK=off "$HOME/go/bin/go" -C runner vet ./internal/server ./cmd/server
# All builds and vet passed.

# From contracts:
PATH="$HOME/go/bin:$PATH" GOWORK=off "$HOME/.cache/blerg-tools/v2.13.1/golangci-lint" run --allow-parallel-runners ./desktopupgrade/... ./releasegate/... ./agentsmanifest/... ./cmd/blerg-upgrade/... ./cmd/blerg-release/...
# From runner:
PATH="$HOME/go/bin:$PATH" GOWORK=off "$HOME/.cache/blerg-tools/v2.13.1/golangci-lint" run --allow-parallel-runners ./internal/server/... ./cmd/server/...
# Both lint runs: zero issues.

bash install/desktop/hostcheck_test.sh
BLERG_SKIP_KUBECTL=1 bash install/desktop/compose_test.sh
bash -n install/desktop/blerg-up.sh scripts/release-public.sh scripts/release-public_test.sh
bash scripts/scrub.sh
git diff --check
# All five checks passed.
```

Initial failures reproduced the review bugs. The real dump drill additionally
caught PostgreSQL's trailing comment delimiter after the completion line; its
regression test now covers that and optional restriction terminators. An initial
shell run selected the old system Go; the rerun used the requested toolchain.
An initial lint invocation used the repository root with workspace mode disabled;
module-directory invocations resolved it. Initial lint findings were fixed.

No frontend changed; TypeScript and browser suites were not run. No full browser
or performance gate was run. The coordinator retains the integrated main gate.
The changes are committed on the assigned work branch; main and public remotes
are untouched under the later branch-only instructions.
