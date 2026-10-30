# Session acceptance before a release

Run this gate on Linux/WSL before a release candidate is pushed to public CI,
then keep the summary and logs with the release evidence. These tests exercise
real processes and an installed runner; the model campaign is deliberately outside
normal CI. See [the release steps](../CONTRIBUTING.md#versions-and-releases) and
[workspace release gates](workspace-integration.md).

## Deterministic daemon scenarios

From `runner/`, with Go 1.26+, a C compiler, Git and ptrace available:

```sh
export GOWORK=off
helper_dir=$(mktemp -d)
go build -o "$helper_dir/daemon" ./cmd/daemon
BLERG_HOST_HELPER_TEST_BINARY="$helper_dir/daemon" go test -count=1 -v \
  ./internal/daemon -run 'TestSessionEndToEndProcessShapes|TestHostSteeringIdleRespawnFreeze'
rm -rf "$helper_dir"
```

Run **outside** a traced agent session: a tracer cannot itself trace these helpers
when another tracer already owns its processes. Use the installation's external
test launcher if working inside a session. Keep build/test worker limits from
`BLERG_TOOL_WORKERS`; do not increase them to the host CPU count.

`TestSessionEndToEndProcessShapes` builds a small Go stub and the existing C
containment-history companion in temporary directories. Commands select these
histories: a thread-group leader exits with a live worker, 160 locked OS threads,
vfork/exec, vfork/exit, POSIX spawn, 1,500 iterations of short-lived fork/vfork/thread
births (which exercise child-before-creator event ordering), 300 Go `os/exec`
children, and normal idle exit. Kernel scheduling decides the actual event order;
the storm stresses it rather than claiming to force one order on every run.

Each of two real daemon-managed child worktrees runs all eight shapes and five
turn ends per shape: 80 commits/synchronizations. Every turn uses the workspace's
real freeze/sync/thaw and result collector, checks the delivered commit HEAD, then
freezes again and compares helper accounting with an independent kernel task
scan. The diagnostic `Group.Census` request runs on the tracer thread and reports
`tracked` and `live` through the inherited control socket; normal freezes do not
scan `/proc`. Census is diagnostic, never a containment proof. The existing
concurrent-child assertions also check collected branches, committed contents,
and the parent's HEAD, branch, raw index, working-tree status and fixture bytes.
The steering test covers the actual streaming driver and message after idle exit.
The shape test drives workspace turn boundaries directly, without a model or a
synthetic engine wire protocol.

## Real sessions on an install

Use a disposable, small, single-module Go repository already discoverable on the
chosen desktop. The daemon and source checkout must be local to this command so
it can check Git objects, worktree cleanup and the original checkout's raw index
and files. No pushes or merges are requested. Clean and dirty parent checkouts
are supported; ignored files are included in the preservation check. Do not run
other writers, editors or Git commands against that checkout during the campaign.

Prerequisites:

- A running runner and Linux/WSL daemon with Hermes installed and a working local
  model profile. Configure that daemon's Hermes provider endpoint/profile from
  your environment, outside this repository. Set `E2E_MODEL` to its registered
  routing/model ID. Children use the same model on the daemon's default profile.
  `E2E_HERMES_HOME` optionally selects the parent's profile, but does not configure
  a remote daemon or change a child's profile. Keep the two profiles consistent.
- An agent token created in **Settings → Agent tokens**, preset **run-sessions**.
  Supply it in `E2E_AGENT_TOKEN`; do not use the session's own messaging token.
- The same owner's signed-in browser session cookie in `E2E_OWNER_COOKIE` (the
  complete Cookie header value), supplied privately through the environment.
  This is additionally required because `/api/sessions/{id}/live`, ask replies and
  top-level discard intentionally refuse agent tokens. The harness does not
  bypass that boundary or save either credential. Never paste cookies into a
  command argument, checked-in file or shared log.
- Delegation policy allowing two parallel Hermes host children on this repo and
  endpoint, and enough session/budget capacity. Otherwise the run fails with logs
  showing the pending approval; it never approves requests itself.
- A continuously appended daemon log file readable by the harness. Capture it to
  a private file before starting if your daemon logs to a service journal. Log
  rotation/truncation fails the run instead of hiding diagnostics.
- An idle-exit policy on the selected engine/install and a daemon-log regular
  expression proving that **this session's** engine exited. Set
  `E2E_IDLE_EXIT_REGEX` with a literal `{session}` placeholder; the harness inserts
  the escaped session ID. A delay alone is insufficient. If the installed Hermes
  gateway stays alive indefinitely, this case times out and remains an unmet
  acceptance gate; do not substitute a generic exit pattern or call it passed.

Set these environment variables using your installation's values:

| Variable | Value |
| --- | --- |
| `E2E_RUNNER_URL` | Runner base URL, without a trailing slash |
| `E2E_AGENT_TOKEN` | run-sessions agent token |
| `E2E_OWNER_COOKIE` | Owner's browser Cookie header value |
| `E2E_REPO` | Discoverable repository name |
| `E2E_CHECKOUT` | Absolute path to its original checkout |
| `E2E_MODEL` | Hermes model/routing ID for the configured endpoint |
| `E2E_DAEMON_LOG` | Private, continuous daemon log capture |
| `E2E_IDLE_EXIT_REGEX` | Session-specific idle-exit log pattern containing `{session}` |
| `E2E_DAEMON_ID` | Optional explicit desktop pin |
| `E2E_HERMES_HOME` | Optional parent Hermes profile path on that desktop |

Then run from the repository root:

```sh
scripts/e2e-sessions.sh --logs "$E2E_LOG_DIR"
# Optional paid/authenticated engine smoke (two tiny turns each):
scripts/e2e-sessions.sh --logs "$E2E_SMOKE_LOG_DIR" --smoke
```

The output directory must not already exist. Python 3 and Git are the only harness
dependencies. The shell entry point with a standard-library Python driver keeps
HTTP polling, JSON, deadlines and failure cleanup readable, without a Go build tag
or an extra package installation on the person's desktop. `--timeout` bounds each
wait (default 1,200 seconds); HTTP requests have a 60-second timeout.

The command prints one table (scenario, PASS/FAIL, seconds), returns nonzero on a
failure, and saves every API response, `summary.json` and the daemon log slice in
a private directory. It checks these scenarios in sequence:

1. Start a real host workspace coordinator through `/api/runner/start`.
2. Ask it to start two parallel Hermes host children that each build, test and
   commit a marker. Require overlapping turn timestamps and verify each returned
   workspace HEAD contains the marker, a zero build status and passing Go test JSON
   in the source Git object database.
3. Wait for session-specific engine exit evidence, send another message and
   require a new turn end.
4. Ask a child to run `blerg-runner ask`; require the person's live state to show
   both an open ask and a positive Needs you count, answer it, then finish the turn.
5. Ask the parent to discard both children through its session CLI; require ended
   state, archived children absent from the live list and removed clean worktrees.
6. With `--smoke`, send two tiny turns each to Claude and Codex. These check startup,
   turn completion and the same log/checkout invariants; child commit collection
   is covered by the Hermes campaign rather than repeated in these tiny smokes.

After each scenario and during waits, require the original checkout's HEAD,
branch, raw index and tracked/untracked/ignored file contents to match its initial
snapshot. All test coordinators are discarded in `finally`, including on failure.
The harness stops on daemon lines containing `writer freeze`, `admission closed`,
`app-server could not start`, `broken pipe` or `not recovered`. Logs can contain
private code and model prose: keep them local, review and sanitize before sharing.

The root coordinator deliberately does not edit or commit in its host checkout;
builds and commits happen in child worktrees, where isolation can be asserted.
An HTTP success or model claim alone is not treated as collected work.

## First-slice limits

This is BLG-342's first slice, not the full card's restart campaign. Daemon restart
mid-turn and restart after a failed freeze are not automated here; existing
`TestHostRestartResumesWorkspaceAfterFailedFreeze` and recovery tests cover those paths
at the daemon level. Run the daemon package suite as well. The real-session
campaign still needs the person's install credentials and endpoint and cannot be
certified from an unattended child session. A full browser interaction test of
the Needs you UI is also separate; this harness checks the live state it renders.

For the harness's credential-free checks:

```sh
python3 -m unittest discover -s scripts -p e2e_sessions_test.py -v
bash -n scripts/e2e-sessions.sh
scripts/e2e-sessions.sh --help
```
