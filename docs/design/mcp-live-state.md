# Native MCP connection tracking

The `mcp-live-state` branch implements the daemon, protocol and server portion of
live native MCP readiness for 0.3.0. Browser rendering is a separate release-session
change. The wire contract and configuration are in [the Runner guide](../../runner/README.md#mcp).

## Native sources and evidence

| Engine | Source | Evidence and limits |
|---|---|---|
| Claude Code | Persistent stream-json control request `request.subtype: mcp_status`; response `response.mcpServers` | Installed CLI 2.1.302: an isolated local MCP fixture moved from pending/tools 0 to connected/tools 1 without initialize or any user prompt. Bundled native SDK and handler confirm this reads current clients. Sandbox images currently pin 2.1.284; the actual installed-CLI probe does not claim to verify that older binary. Synthetic tests exercise sandbox command routing. |
| Hermes | Native cached `mcp.servers.status` and loaded `session.info.mcp_servers` | Desktop gateway contracts 6 and 8. Reads never connect/probe/start auth. `connecting`/`configured` remain pending; `lazy` stays explicitly on demand and settles without claiming a live connection. Native process generations invalidate prior inventory. |
| Codex | App-server `mcpServerStatus/list`, current `threadId`, paginated `data` and `runtimeStatus` | Installed CLI 0.162.1 generated protocol types. Synthetic native app-server fixtures verify pending → connected, pagination, initialization and native process replacement. Global pre-thread inventory remains provisional, even when empty. No provider/model call was used. |

Legacy Claude per-turn mode has no persistent read-only control source. An explicit
native init inventory can be retained as a fallback when an older persistent CLI
rejects the status request; unsupported discovery never becomes an invented empty
or connected inventory. Failed or truncated inventory reads remain unavailable.

## Shared implementation

The optional `mcpStatusSource` and `SetMCPInputGate` interfaces separate native
adapter facts from polling, serialization and first-prompt policy. One tracker
actor serializes durable status events, bounds native reads to one in flight,
polls each native process no faster than every two seconds, and stops after
settlement or 120 seconds. A cheap generation getter detects process replacement
without continuing native RPCs after settlement. A finished tracking budget does
not rewrite an unresolved server's native status.

The first-input gate runs before input removal/delivery. Its total budget is at
most 30 seconds, including initial discovery (up to six seconds when no servers
are known). Configuration can shorten or disable it. Readiness, timeout or the
owner releases it; later inputs pass immediately. Canceled native processes retain
the undelivered input and its first-input entitlement. A restored thread identity,
metadata snapshot or workspace note alone does not imply a previous prompt.

Native RPC deadlines do not own the engine lifetime. In particular, Claude process
preparation receives the session context, rather than a two-second polling context.
The pod closes its AgentHost on every exit path, including startup failure.

## Security and compatibility review

- Both adapters decode only public names/status/source/tool counts. Shared protocol
  sanitization runs again on the server before persistence and fanout, including
  malicious events marked transient. URLs, headers, credentials and native errors
  are excluded. Partial/invalid inventories cannot silently become known-empty.
- Gateway sessions expose their gateway metadata; native restricted execution and
  grant policy remain responsible for containment. No model prompt discovers tools.
- Browser release requires the current session owner, an active daemon and the
  existing current-runtime write fence. It cannot launch or resume a session.
  Cluster pods additionally reject release commands for another session ID.
- Latest snapshots use a dedicated partial index and `LIMIT 1`; privacy registration
  covers `mcp` and `mcp_hold`. Expired/ended/disconnected holds are removed. Clients
  must also expire cached holds at `until`, including when a live read returns 304.
- Older daemons omit the additive state. Existing generic event transport accepts
  unknown kinds; an older server can ignore the new event without a protocol error.
- Independent adversarial reviews covered adapters, tracker cancellation, durable
  IDs, server authorization/privacy, snapshot retrieval and host/pod routing. Findings
  were fixed before handoff. A mutation test reproduced the canceled-gate defect
  against the old behavior and passed with the fix.

## Verification

Focused real-Postgres protocol/live/server/DB race tests and vet passed. Adapter,
tracker and routing races passed repeated runs, including held-input retention,
settle/timeout/person release, unknown inventory, generation changes, gateway scope,
invalid/transient status events, owner-only release and old-peer behavior.

Final gates passed with isolated Postgres databases and Go 1.26.9 / `GOWORK=off`:

- Full Runner `go test -count=1 -timeout 30m ./...`: 318.15 seconds.
- Full Runner `go test -count=1 -timeout 30m -race ./...`: 336.91 seconds.
- Latest affected daemon/pod package race run: 71.38 / 3.22 seconds, including
  the final shared inventory guard and first-input discovery-deadline regressions.
- Full Runner `go vet ./...`, pinned v2.13.1 `make lint-go` across all four modules,
  `bash scripts/scrub.sh`, `bash scripts/check_links.sh` and diff checks: clean.

Full-suite timings overlap normal/race runs and reflect shared-disk load; they are
validation timings, not the isolated performance comparison from item 1. Native
provider sessions, the owner's daemon/service, frontends and release branches were
not modified or restarted by this work.
