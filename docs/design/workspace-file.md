# Workspace-file safety and implementation review

The `workspace-file` branch implements the daemon/server primitive for
`GET /api/sessions/{id}/workspace-file?path=relative`. Browser links and rendering
are owned by the separate release-session frontend work. See the
[REST contract](../../runner/README.md#opening-a-session-workspace-file) and
[OpenAPI route](../../runner/internal/server/openapi.json).

Status: APPROVED for the implemented contract, no outstanding blockers. Read-only review covers protocol, human REST authorization, broker correlation/current runtime authority, daemon/terminal/pod bindings, secure reader, shared classification, response validation, HTTP and websocket writes, resource bounds, privacy registration and route documentation. All filesystem fixtures used temporary directories; Postgres used isolated test databases. The owner's daemon/services and provider/model sessions were not used.

## Findings fixed during review

1. Pending-RPC counts originally stopped bounding memory after daemon replies, leaving unlimited slow HTTP responses retaining full file buffers. Full-handler leases now remain held through response completion: two globally and two per daemon ID, including reconnects. Strict streaming validation retains only a 512-byte decoded head rather than whole decoded/re-encoded copies.
2. Immutable workspace path text alone allowed directory retargeting before a read to expose a different folder. Agent and terminal launches now capture directory identity; expectedRoot is checked against canonical and pinned roots inside the reader, not merely before/after it. Reattached terminals use their trusted stored ProjectPath or are unavailable. Private Go-overlay mutation omitting expectedRoot produced a real failure containing synthetic out-of-workspace bytes; fixed-source test passes.
3. Generic websocket sends could block indefinitely behind large file responses. The new file-only sender acquires write ownership without waiting, sets a 10-second deadline, closes a failed/timed-out connection and preserves ordinary sender behavior. The daemon's two permits cover reads AND sends.
4. Root.OpenFile does not provide the desired final-symlink guarantee merely by passing O_NOFOLLOW. The Unix implementation uses direct descriptor-relative Openat with no-follow on every parent and final component, plus final O_NONBLOCK and inode checks.
5. A small error response needs a fresh bounded write grace after exhausting the daemon deadline; otherwise an old peer produced EOF instead of JSON409. Fixed refusal writes receive at most one additional second; successful large responses retain original/fenced deadlines. A responseStarted guard prevents a late fence-commit error appending a second JSON document after a200 response.
6. Intermediate link components cannot be cleaned away before secret checks. The resolver walks targets componentwise, preserving hidden-link/../public traversal until every protected intermediate target has been checked.

## Rule/test matrix

Test names below are in runner/internal/daemon unless another package is named.

| Rule | Mechanism | Tests/evidence |
| --- | --- | --- |
| Relative paths only; reject traversal, encoded traversal, absolute/~, NUL/backslashes/control characters | Request validation before filesystem I/O; decode only to detect unsafe encodings, keep actual safe literal OS names | TestWorkspaceFileRejectsTraversal; server.TestWorkspaceFileReplyValidationAndAccess |
| Use session's original folder; host mount path for sandbox; no docker exec to read | Launch directory identity and immutable host path; expectedRoot checked inside reader | TestAgentWorkspaceReadUsesOwnFolderAndNeverResumes; TestManagerWorkspaceReadRoutesAgentAndTerminalFolders; TestWorkspaceHostRejectsFolderRetargetedBeforeRead |
| Cluster commands stay bound to pod session | Matching session gate plus server current immutable launch fence | runner.TestPodWorkspaceFileUsesBoundSessionFolder; server.TestWorkspaceFileReplyValidationAndAccess (invalid runtime claim) |
| Parent/full-path symlinks cannot escape | Componentwise resolution confined to pinned os.Root; full EvalSymlinks containment; direct no-follow descriptor walk | TestWorkspaceFileSymlinkEscapesAndSecretAliases; TestWorkspaceFileSwapsBetweenCheckAndOpen; TestWorkspaceFileAdversarialChangesAfterOpen |
| In-root benign links work; secret aliases/chains do not | Check requested/resolved/every intermediate target; do not erase link/.. components | TestWorkspaceFileReadAndClassification; TestWorkspaceFileAdversarialIntermediateSecretBeforeParentTraversal |
| All specified secret globs/directories, case-insensitive | One policy shared across requested/resolved/link target paths | TestWorkspaceFileSecretPatterns; TestWorkspaceFileSymlinkEscapesAndSecretAliases |
| Regular files only; no dirs, FIFO/socket/device reads | Precheck type; final O_NOFOLLOW/O_NONBLOCK; fstat regular before read | TestWorkspaceFileRegularOnlyAndHardlinks; TestWorkspaceFileSwapsBetweenCheckAndOpen (FIFO swap) |
| Refuse current hardlink aliases | Nlink must equal1 before/after reading | TestWorkspaceFileRegularOnlyAndHardlinks; TestWorkspaceFileAdversarialChangesAfterOpen/new_hardlink |
| Close check/open/read race windows | SameFile identity, pinned/current root identity, postread size/mtime/Nlink checks, final path re-resolution | TestWorkspaceFileAdversarialRegularInodeSwap; TestWorkspaceFileAdversarialChangesAfterOpen; TestWorkspaceFileSwapsBetweenCheckAndOpen |
| Original folder cannot be replaced before read | expectedRoot launch identity compared at reader's actual root open boundary | TestWorkspaceHostRejectsFolderRetargetedBeforeRead; private negative-control run |
| Engine end/replacement cannot yield stale launch bytes | Live pointer/context/runDone checks before and after I/O | TestWorkspaceWorkerRejectsLaunchEndedDuringRead; TestAgentWorkspaceReadUsesOwnFolderAndNeverResumes |
| Human owner or account.manage admin only; no agent/service token path | authBrowser plus human-kind check, owner/admin guard before daemon dispatch | server.TestWorkspaceFileOwnerOnlyAndOldDaemonTimeout; server.TestWorkspaceFileReplyValidationAndAccess |
| Request/response authority cannot be confused across sockets/session/reconnect/launch | Random request ID, exact daemon pointer and session correlation, current ownership checks, final runtime fence | server.TestWorkspaceBrokerBindsConnectionSessionAndConsumesOnce; server.TestWorkspaceBrokerLimitsDisconnectAndReplacement; server.TestWorkspaceFileReplyValidationAndAccess |
| Old/missing daemon bounded, no implicit resume | Ten-second server wait; missing binding409; no engine preparation | server.TestWorkspaceFileOwnerOnlyAndOldDaemonTimeout; server.TestWorkspaceFileTimeoutReturnsJSONOverRealHTTP; TestAgentWorkspaceReadUsesOwnFolderAndNeverResumes |
| Text/default2MiB; image/PDF25MiB; env-configurable within absolute25MiB | Classify bounded head; cap+1 total read; oversize413 with no partial bytes | TestWorkspaceFileBoundsAndGrowingFiles; TestWorkspaceFileLimitsEnvironment |
| Response bytes and metadata are consistent | Exact clean requested path, size range, strict canonical base64/no CRLF, exact streamed decoded length, mtime validation; server reclassifies bounded head | server.TestWorkspaceFileReplyValidationAndAccess (head and long-tail malformed encodings included) |
| Memory and goroutine bounds extend through sending | Two daemon read/send permits; two full-handler global/per-daemon-ID leases; existing64MiB websocket cap fits25MiB base64 | TestWorkspaceWorkerBoundIncludesBlockedResultSend; server.TestWorkspaceReadLeaseHeldUntilHTTPResponseCompletes; server.TestWorkspaceReadLeaseGlobalAndReconnectLimits |
| File sends cannot hang all control indefinitely | Scoped writer TryLock, deadline, close on write failure | TestWorkspaceWireDoesNotWaitBehindOtherWriters; TestWorkspaceWireTimeoutClosesStalledConnection |
| Safe browser/privacy surface | JSON only, private/no-store and nosniff, owner/admin boundary, no transcript artifact broadcast, scoped protocol registry | server.TestWorkspaceFileReplyValidationAndAccess; existing privacy guard/scope tests |
| No content/raw OS error logging | One quoted metadata-only log in queueWorkspaceFileRead; sanitized fixed protocol errors | Source review of queueWorkspaceFileRead/workspaceFileError/server response mapping; synthetic failure tests assert no returned file data/raw error text |

## Independent validation

- Own new files: workspacefile_adversarial_test.go and workspacehost_adversarial_test.go. Source frozen; no production edits by this reviewer.
- Reader adversarial plus original reader tests: race-count10 passed1.587s.
- Reader, host and worker focused suite: race-count20 passed3.062s.
- Private overlay omitting immutable launch identity: RED at explicit original-folder assertion, with synthetic outside bytes returned (private negative-control run). Shared source was never mutated.
- Server reviewer reports real-Postgres race repetitions, actual-network refusal-grace negative control, and streaming decoder long-tail validation; root owns final full Runner tests/vet/lint/scrub/links.
- Darwin arm64 cross-compilation passed. Linux runtime tests were executed; native macOS execution was not available. Unsupported platforms fail closed.

## Residual limits

- Every current Nlink>1 file is refused, even harmless duplicate links inside the workspace. This is deliberate because pathname checks cannot establish a hardlinked inode's other names. An ordinary copied file or inode with only one current link is governed by its current path; no historical-origin/content-secret detection is claimed.
- Filename policy blocks conventional secret locations, not secrets copied into an otherwise ordinary allowed file. No content scanning/redaction is part of this contract.
- Linux/macOS secure primitives are implemented; other platforms refuse. This does not establish a protection boundary against a privileged actor changing mount namespaces, arbitrary daemon process memory, or workspace filesystem semantics.
- Regular-file I/O on a stuck filesystem may not be interruptible. Two workers bound this exposure and unrelated daemon command processing remains responsive; the HTTP wait still ends after10s with at most1s fixed-error write grace. Disk workers are not claimed to stop in10s.
- Full files over their effective caps receive413; truncated output is not supported. Env values cannot raise the absolute25MiB bound, keeping encoded frames below existing64MiB transport cap.
- Artifact view safety policies must still be honored by the separate frontend viewer. This backend returns JSON/base64, never serves active file content directly on the app origin.

## Final integration gates

The complete Runner validation passed with isolated Postgres and Go 1.26.9 / `GOWORK=off`:

- `go test -count=1 -timeout 30m ./...`: 276.58 seconds.
- `go test -count=1 -timeout 30m -race ./...`: 299.15 seconds.
- Full Runner `go vet ./...`, pinned v2.13.1 `make lint-go` across all four modules,
  `bash scripts/scrub.sh`, `bash scripts/check_links.sh` and diff checks: clean.
- Current Darwin arm64 daemon test binary cross-compiles; Linux executes the runtime tests.

Normal/race timings overlap and reflect shared test-disk load. No protected frontend,
chat-package or messaging-CLI files were changed. `go mod verify` passed; the
new direct `x/sys` requirement uses v0.47.0, already selected by the existing
module graph. Classification is shared with published artifacts through
`internal/fileview`; their origin-serving policy remains in the artifact routes.
