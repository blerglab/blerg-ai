# Charts as first-class citizens

Design note for BLG-508, November 4, 2026. Phase 1 only: no implementation or new delivery tickets in this change. Claude design review is required before building. The whole epic ships in **0.6.0**; the date may slip. The proposed defaults below are engineering decisions for review, not additional owner approvals.

Revised after Claude review round 1 of `401e3ea7`: native-only live charts, explicit scheduled authority and retention, strict token policy, and staged delivery. This revision still requires review; it authorizes no implementation.

Charts are a primary way to read and work with Blerg data. They must feel composed, readable, responsive, and alive in chat, tabs, split panes, dashboards, and exports. A dashboard is a document in the existing file/tab system, not another navigation destination or attention mechanism.

## Existing foundation and boundaries

Read together with [chart tabs](blg-451-chart-tabs.md), [our chart renderer](blg-452-chart-renderer.md), and [our diagram renderer](diagram-renderer.md). Preserve their policy checks, isolated compilation, bounded resource budgets, export contract, conservative classifier, and static fallback to Vega for allowed specs our renderer cannot draw correctly. Live specs follow the native-only rule below. Policy refusal never falls through to a less restricted engine.

At this checkout, `packages/chat/src/visual/render.ts` dispatches validated specs to our SVG renderer or the bounded Vega worker. `RenderedVisual` returns SVG, size, disposal, and optional toolbar. `chartSize.ts` calculates fit/fill; `theme.ts` resolves inherited theme tokens. The Vega path compiles a snapshot, terminates its worker, sanitizes SVG, and displays a canvas. Our SVG path also constructs a drawing per render. Neither return contract exposes a persistent data changeset operation. A new mounted renderer lifecycle is necessary; changing React props alone does not meet the live requirement.

BLG-507 owns narrow layered sizing, shared layer color domains, and title wrapping; keep its regression fixtures and correct visible behavior. B/C replace its interim sizing/title heuristics with measured retained layout; do not fossilize those heuristics as architecture. BLG-506 owns lazy rendered artifact previews, live preview refresh, refusal-with-source-toggle, and a quiet right-aligned relative timestamp with exact time on hover. Extend those paths for dashboards. Do not duplicate or edit those workers' files in this design phase. BLG-483's motion/hover, BLG-485's card references, and BLG-472/474's theme work must be reconciled with the delivery slices below before filing overlapping work.

## Dashboard document

A `.dash.json` is a bounded, declarative UTF-8 JSON document, recognized before generic JSON. This is a proposed format, distinct from Vega-Lite. Only the embedded `spec` is subject to Vega-Lite grammar. Data resolution happens outside that spec; agents still cannot put URLs, expressions, executable mappings, or secrets in a chart.

```json
{
  "format": "blerg-dashboard",
  "version": 1,
  "title": "Release activity",
  "cards": [
    {
      "id": "merged-today",
      "kind": "stat",
      "title": "Cards merged today",
      "span": 1,
      "data": {
        "kind": "dataset",
        "name": "blerg.cards.merged",
        "parameters": {"release": "0.6.0", "bucket": "day", "timezone": "America/New_York"}
      },
      "view": {"value": "count", "unit": "cards", "selection": "latest"}
    },
    {
      "id": "merges",
      "kind": "chart",
      "title": "Merges through the release",
      "span": 3,
      "data": {
        "kind": "dataset",
        "name": "blerg.cards.merged",
        "parameters": {"release": "0.6.0", "bucket": "day", "timezone": "America/New_York"}
      },
      "spec": {
        "mark": "bar",
        "encoding": {
          "x": {"field": "bucket", "type": "temporal", "title": "Day (ET)"},
          "y": {"field": "count", "type": "quantitative", "title": "Cards merged"}
        }
      }
    }
  ]
}
```

The document inherits the active theme; no stored theme ID, CSS, color, palette, or scheme. `id` is a unique stable card identity; array order is reading and keyboard order. `span` is an integer 1–4. Each card has its own `data` reference: `dataset` as above, `mcp` with an opaque `bindingId`, `file` with an authorized artifact identity and optional bounded rows path, or `inline` with `rows`. Text cards also have a data reference, even if it is one inline row containing their text. Shared references can share an authorized subscription without sharing a card's view state.

The backend validates the whole envelope atomically before publication. Unknown version or kind, duplicate IDs, invalid spans, missing fields, invalid embedded specs, and over-budget documents receive named errors and safe cause logs. Proposed initial caps: 32 cards, 1 MiB document, 10,000 rows per card, 50,000 rows per dashboard; embedded specs must also pass existing stricter chart limits. These limits do not raise the existing renderer budgets. Unsupported future major versions remain downloadable through Source; never render them approximately. Version 1 rejects unknown structural fields. Compatible reader fixes do not change the format version; new incompatible semantics require a new document version, preserving the original.

### Layout and card kinds

Use CSS grid with container queries on the **pane content width**, not window width: 4 columns at 1024 px and above, 2 at 520–1023 px, 1 below 520 px. Effective span is `min(span, columns)`; stable placement follows array order without dense packing. Gaps are intentional, never a reason to reorder accessible content. At 390 px every card has the full usable width; at 3840 px the grid fills the pane instead of stopping at a desktop max-width. A span-4 trend can use the whole display. Controls retain natural size, typography stays readable, and extra space goes to data, ticks, and comparisons.

Each native card has one anchored header, consistent inset, surface, radius, and optional footer for source freshness. Header wraps; it is outside plot bounds and never clipped. Body uses `min-width: 0`. Content determines row height with kind-specific minimums: compact stats, readable charts, multiline text. Charts reserve plot height before lazy loading; long dashboards scroll vertically. Reflow does not scale text down or squeeze a chart to a sliver. A complex composition may require a taller card or full-tab view; it must not force the surrounding pane to overflow horizontally.

| Kind | Body and data contract |
|---|---|
| `stat` | Value, unit, optional comparison with named baseline and small sparkline; zero and unknown are different. Explicit latest/single-row selection, never silent summation. |
| `gauge` | Numeric value and declared finite minimum/maximum, unit, optional labeled thresholds; bar or arc plus exact text. Missing value is unknown; out-of-range values retain the true number and an overflow indication. |
| `chart` | Embedded allowed Vega-Lite spec. The adapter inserts resolved rows as inline data into an internal validated copy; no competing authored data source. |
| `text` | Plain text or existing sanitized restricted Markdown from a declared field; no raw HTML, scripts, or expression interpolation. |

Heatmap, status timeline and histogram are **chart templates**, not additional card kinds. Heatmap rows declare row/column/value fields, units and ordering, distinguishing unknown from zero. Timeline rows declare entity/start/end/status, timezone, open intervals and unknown gaps. Histogram rows declare precomputed lower/upper bin bounds, counts, units and inclusive-edge rule. These use `kind: "chart"`, native supported marks and the same embedded spec contract; preprocessing is a named server aggregation, never arbitrary transform code.

Native forms share the same data store, accessible summaries/table, interaction rules, motion, and export pipeline. They do not masquerade as floating Vega text marks. A dashboard exports a themed grid as SVG/PNG using the current authorized snapshot and freshness labels; source download never includes credentials. Native card exporters are new work, not assumed to exist in `RenderedVisual` today.

### Tokens and editing

Use existing resolved `VisualTheme` values: `surface` for the dashboard, `surface2` for cards, `fg` for values and titles, `muted` for units/axes/freshness, `border` for dividers/grid lines, `accent` for focus/selection, and `palette` for stable series identity. These map to `--chat-surface`, `--chat-surface-2`, `--chat-fg`, `--chat-muted`, `--chat-border`, `--chat-accent`, and the current token-derived visual palette. Reuse existing font family/size, radius and spacing tokens; where the design system lacks a semantic status, sequential scale, motion, or spacing token, add it centrally with values for **every** theme before consuming it. No component fallback literal or chosen theme ID. Theme switching updates mounted cards, tooltips, focus, exports, and legends together. Status uses the shared semantic tokens, never red/green alone.

### Enforced theme policy

A promise in this format is insufficient: `vegaPolicy.ts` currently permits authored ranges/background through `checkRange`/`checkConfig` (around lines 407–460). A1 changes that shared validator before accepting dashboard specs. Recursively refuse authored color, fill and stroke literals in marks, encodings, conditions, config/style/axis/legend settings; refuse color/fill/stroke `value` or `datum`, conditional value branches, `scale.range`, `scale.scheme`, `config.range`, and `config.background`, plus equivalent root background overrides. Allow field-based color semantics only; data cannot smuggle literal paint through a color channel. Report `theme-owned-color` with the offending structural path, never source data. Apply the same gate to embedded specs, standalone specs, live-file replacements, binding-fed specs, imports, previews and exports before either renderer runs. Test nested conditions and all layers/compositions, and mutation-test the refusal guard. Do not strip overrides and quietly change meaning.

`vegaTheme` supplies token-derived sequential, diverging, ordinal and heatmap ramps **after** source validation as trusted renderer configuration. Native adapters consume those same ramps. Extend the central theme contract to at least eight categorical colors distinct from the focus/selection accent; accent never doubles as a series hue. Validate text contrast at 4.5:1 and meaningful mark/focus contrast at 3:1 against **surface2**, including hover and color-blind themes. Beyond eight series, reuse hues only with stable dash/shape/pattern combinations and explicit legend labels; default templates offer filtering/small multiples before a crowded overlay. Never cycle hues without another distinction or drop series silently. Domain-to-style assignment remains stable as data or filters change.

Regular republish under the same filename creates a new version in the existing tab and Follow latest flow. `publish --live` updates the live identity without accumulating file versions. Document revisions and data sequence numbers are separate: a new sample does not create a document revision. Invalid replacements retain the last valid authorized dashboard with a quiet failure indication. Historical source views pin document bytes; explicitly mark live referenced data as current, not historical. A frozen export records document revision and per-source as-of timestamps.

Main can edit one card by stable ID, producing a validated revision with an optimistic base revision. A conflict reports the changed card and requires a refreshed edit, never overwrites another edit. Unchanged cards retain DOM identity, subscriptions, hover, scroll, and focus; changed card configuration is staged and atomically committed. Removing a card releases its subscription. Reordering preserves identity. A source toggle is one click away in the existing utility bar; add no new dashboard settings destination. Binding schedule/connection settings live in the existing settings panel, with a contextual link from the card.

## Renderer and layout engine

The pane owns the outer box; the dashboard owns grid placement and card chrome; each card owns its content box; the chart layout engine owns title/legend/axes/plot geometry inside that box. There is one authoritative measurement per level. Renderers do not infer dimensions from partially mounted descendants or fall back to 640 px for a hidden pane.

1. The card shell reserves its layout before loading. A layout effect obtains the initial positive content rectangle after headers/insets. A shared `ResizeObserver` observes card bodies and the pane. Font readiness and theme typography changes invalidate metrics. A zero-size/hidden surface defers its first render and retains its prior prepared state.
2. The layout engine measures text using the resolved font, computes wrapped headings, legends, tick density, axis margins, and final plot rect in a bounded two-pass calculation. Explicit composition panel sizes remain meaningful; unsupported static combinations go to the bounded Vega path; unsupported live combinations show `unsupported-live` with a static snapshot. BLG-507's shared color domain and title behavior are invariants.
3. Initial geometry is prepared offscreen. Only a complete drawing for the latest size/theme/source generation can become visible. A generation fence discards late workers and image decodes. Loading is a reserved shell, never a visible provisional 640×360 drawing.
4. During splitter drag, the existing scene stays mounted. The observer coalesces measurements into one geometry update per frame; before paint, native SVG geometry, hit regions, axes, and viewport are committed together. No clear-first effect, debounce that leaves stale geometry visible, or dispose-before-replacement. Resize has no entrance animation. If a fallback needs asynchronous recomputation, retain its prior complete drawing fitted inside the new viewport until a complete exact layout swaps in; it may have reduced detail temporarily, never overflow, distort, blank, or expose a wrong-size intermediate scene. Tests distinguish this explicit retained snapshot from a newly painted layout with wrong dimensions.
5. For tab transfer, keep a bounded retained snapshot in the destination shell while preparing its measured view. Preserve the model/subscription across transfer; release the old surface only after the destination commits. A closed tab cancels work and disposes its owned surface without affecting another view.

Extend the lifecycle with a mounted handle: `resize(box, generation)`, `applyData(changeset)`, `setTheme(tokens)`, `snapshot()`, and `dispose()`, while retaining the existing export contract for static callers. `snapshot()` serializes the current committed scene, not the initial SVG string. The public shape must be settled in the contract card before adapters change.

### Data-only updates

Normalize rows with stable keys (source-declared primary key; otherwise explicitly configured key fields). A changeset has source ID, epoch, base sequence, next sequence, upserts, removals, observed time, and status. It updates the mounted scene's data store and keyed marks; it does not call the source compiler, replace the chart host, or recreate axes/legend. A changed bar grows from its old value, a line extends to the new tick, a gauge moves its indicator, and only new marks enter. Retractions and corrections use the same key; late data is sorted by event time. Coalesce bursts to the latest target without dropping removals or counters.

Keep domain policy explicit: fixed session time domain for intraday markets, stable categorical order and color assignment, optional follow-latest window. Expand numeric domains only when necessary, update existing axis nodes in the same transaction, and do not jitter scales on every tick. A rolling window removes expired points explicitly. Preserve scroll and focused/hovered key; if it disappears, dismiss its tooltip and move keyboard focus predictably. A missing sequence requests a bounded full snapshot, reconciled by key into the mounted scene; never silently apply a delta to the wrong base.

B and C are a **retained-mode rewrite of `blerg/svg.ts` and its mark/layout callers**, not a small lifecycle patch: budget 14–18 engineering days combined. Replace string-build/parse-per-render with a keyed scene and persistent SVG nodes, atomic geometry/hit-region commits and serialization from the current scene. Preserve sanitizer boundaries, accessibility and bounded work. B owns scene identity/measurement/export; C owns data reconciliation/interaction/motion. Retire interim BLG-507 heuristics while keeping its tests. Existing budgets apply to every pulse.

**Live specs must be native.** A Vega-only live spec receives the exact reason `unsupported-live` and an explicitly labeled static snapshot with as-of time; no live subscription is started for it. An explicitly requested static snapshot may perform one authorized, audited read under the same quotas and grant; it never schedules refreshes. Static allowed specs may still use the current Vega fallback. There is no persistent Vega worker, live Vega adapter, or slice D. Every shipped live template, including the layered NVDA case, must pass native classification and changeset tests before release. Configuration changes may stage a replacement; data-only updates never do.

Motion defaults proposed for review: one 180 ms entrance opacity/short reveal after first correctly sized paint; 160 ms eased interpolation for changed/new marks; 100 ms hover emphasis. No stagger across dozens of cards, bouncing axes, animated numbers announced every frame, or repeated entrance on data/theme/resize. `prefers-reduced-motion` commits final geometry immediately with no interpolation, fades, or reveals. Screen readers receive throttled meaningful summaries, not animation frames.

### Hover, touch, and focus

Hit-test the current committed plot coordinates, accounting for pane offsets and scaling. Empty plot space has no tooltip. Bars/cells select the actual mark; a line selects the nearest sample within a bounded screen-space distance, not a chart-wide bounding box with one tooltip everywhere. Hover adds a token-derived outline/weight or point marker and subdued sibling emphasis without changing layout or relying on color alone. Layered tooltips name series, exact value/unit, and timestamp/timezone; configured shared-x inspection combines only matching samples and labels missing series honestly.

Tooltip follows its anchor inside the pane, flips at edges, never clips or intercepts dragging. Pointer leave, Escape, scroll, drag, or disappearance closes it. Escape consumes the popover action before tab shortcuts. Keyboard uses roving focus per series and arrows among samples; focus exposes the same detail. Tap pins a value and tapping elsewhere dismisses it; there is no hover-only required information. Existing card-reference chips remain reachable through the established detail interaction. Accessible summary and data table are available regardless of renderer.

## Live data

All three kinds enter one authorized source service and one sequenced socket protocol. The server resolves data into bounded typed rows, schema, key fields, freshness and availability metadata. Browser specs never fetch remote resources. Subscribe checks visibility; each recomputation and fan-out checks it again. Cache and dedup keys include owner, source parameters, connection generation, and authorization scope. Unsubscribe and permission changes invalidate queued deliveries. On reconnect, an epoch/sequence handshake resumes or supplies a new authorized snapshot. Offline views say disconnected with last observation time; a local timer advances staleness even without socket messages.

### Service, authority and restart ownership

**Runner** owns the live-data service: dataset catalog/resolver, durable binding scheduler, normalized row store and socket delivery. **Board** owns card facts and exposes a new authenticated, cursor-based Board-to-Runner event feed with durable event IDs, transition times, board/release identifiers and visibility scope. Runner stores its cursor transactionally with aggregate updates; reconnect replays idempotently. A feed gap requests an authorized snapshot/rebuild rather than inventing missing transitions. Proposed new feed: Board `GET /api/boards/{boardId}/events?after={cursor}`, authenticated by the dataset-scoped Core capability, with bounded pages, visibility/deletion events and an explicit cursor-expired response. This is new service plumbing, not a browser polling loop.

**Core** issues a new short-lived, session-less `live-data` service credential to Runner through authenticated service-to-service identity. It is bound to owner, binding/dataset, connection (when applicable), exact tool/hash, approved binding revision, allowed operation, audience and expiry. Core checks the durable person-approved grant and current connection permission on issuance/refresh; Runner's existing Core service identity is the bootstrap path, never a finished session token. Runner presents this capability to the MCP gateway; the gateway resolves the owner's existing Core-managed connection credential server-side and verifies the grant again. Dataset capabilities allow only the named dataset and authorized Board scope; they cannot execute tools. Proposed new credential endpoint: Core `POST /internal/live-data/credentials`, accepting an authenticated Runner identity and durable grant ID/revision and returning an audience-bound capability with a five-minute maximum lifetime. Renewal repeats authorization, never extends a revoked grant. The MCP gateway gains a session-less binding-grant verifier alongside its session verifier; a capability for one path is invalid on the other. Core's connection credential never enters Runner rows, document source, browser or agent environment. Arguments must be nonsecret; authentication stays in the existing connection.

Each scheduled MCP call and dataset refresh has a durable audit row written **before dispatch**, recording opaque owner/source IDs, grant revision, tool definition hash where applicable, attempt ID, due/start/end, safe outcome/status and row count, never arguments/results/secrets. If the audit write or credential mint fails, do not call. Finish the row on success/failure; a crash leaves an auditable interrupted attempt reconciled after lease expiry. Retries receive new attempt IDs; this does not promise provider exactly-once execution.

Bindings, grants, due times, lease fencing generations, aggregate checkpoints and audit rows live in Runner/Core PostgreSQL, not process memory or session storage. Desktop containers resume from their persistent database volume after restart; cluster Runner replicas use the same durable database and acquire leases with fencing before work. Expired leases are reclaimed once, missed intervals collapse to one due call, and no catch-up storm runs. Fresh credentials are minted after restart, never restored from logs. Deployment without durable database or Core service identity reports unavailable and pauses; an LLM/session is never required for recovery. Audit metadata is retained 180 days, including tombstones needed for accountability, without retaining polled payloads.

### Demand, limits and retention

Per owner: **20 enabled bindings**, **60 dispatched MCP calls/minute** (sliding window), and **2 concurrent calls**. Per connection: **30 calls/minute**, **1 concurrent call**, further reduced by provider limits. Runner has a **4-worker polling pool per installation**, with a shared database semaphore across cluster replicas, a bounded queue of **80 due bindings**, fair owner rotation and 30-second call timeout. Rate-limited work reschedules; a full queue records overload and reschedules without accumulating jobs. Settings can lower these defaults; raising them requires an installation-admin limit change and visible quota documentation. No frontend or agent can bypass them by creating duplicate bindings. Credential, audit and dataset tasks use a separate bounded two-worker queue so a stalled provider cannot block record updates.

Polling requires an authorized visible subscriber. Chat previews subscribe only while intersecting the viewport in a visible document; hidden tabs, collapsed cards and offscreen previews unsubscribe. Full views follow the same document visibility rule. Use a 30-second subscription heartbeat and 60-second lease TTL; after the last lease ends, pause scheduling and cancel/ignore in-flight results. No-view grace is at most 60 seconds; no indefinite background polling. Return to view reacquires authority and fetches at most one current sample. Named event aggregates keep their durable checkpoints, but row delivery and machine sampling subscriptions stop with demand; live-file producers remain responsible for their own jobs.

An approved binding auto-pauses after **7 days without a visible subscriber** and expires **30 days after approval**. Renewal/resume after expiry requires a new person-approved revision; ordinary visibility pause/resume within validity does not. Settings shows expiry and state. Deletion/revocation immediately fences calls and invalidates payload access.

Normalized MCP rows are stored in Runner PostgreSQL keyed by owner/binding/epoch/row key, bounded to **10,000 rows and 5 MiB per binding**, keeping only the latest bounded window, for **24 hours since last successful fetch**. Reject an oversized sample atomically rather than silently truncating it. No raw provider response is persisted. Expiry/deletion/revocation deletes payload rows immediately in the transaction and purges derived caches; automatic TTL deletion runs at least hourly. No-view auto-pause deletes payload rows; transient visibility pause can retain them until TTL. Browser caches are memory-only and cleared on permission loss. Backups follow existing encrypted backup retention (maximum 30 days for these tables), cannot serve revoked rows, and must honor tombstones before restoring service. Document artifact versions retain their existing policy and are not a hidden polling history store. Dataset materializations are rebuildable record-derived caches; source-record retention is unchanged.

### A. Named Blerg datasets

Use a catalog with documented parameters, schema, keys, units, visibility, and triggers. Initial names cover cards done/merged per hour/day by release, gate durations, review findings by author/reviewer engine pair, card time, engine usage, machine load, and disk capacity/free space. Done is a Board state transition from the event feed. Merged means **Git merge commits on `main`**, deterministically enumerated from first-parent `main` history by the authorized repository/daemon integration, keyed by repository and merge SHA, timestamped by committer time. Release membership is an explicit revision/tag range or recorded release association. Squash/fast-forward commits are not counted as merge commits; disclose that coverage. Never infer merges from done cards or milestone prose. Gate durations come from the structured report of `scripts/pre_merge.py` (`seconds` at run level and per-suite `seconds`, outcome, commit and timestamps); an authorized ingestion endpoint stores sanitized gate-run records in Runner, keyed by run ID, with no host/path/log output. Do not derive duration from failure marks or sum overlapping suites as wall time. Historical missing reports are gaps. Idempotent event IDs and correction/reopen rules prevent double counts. Return buckets with UTC boundaries and display timezone; distinguish an observed zero from absent coverage. Unknown engine usage is not zero.

Board/runner records trigger deterministic recomputation; share only within equal authorization scopes. Machine load and disk come from an authorized daemon telemetry publisher, not a new server shell command. Label load average and CPU capacity separately; disk rows expose display labels/capacity, never private mount paths. Unreachable daemons leave a gap. Usage follows provider reset windows and measured as-of time. These sources require no agent tokens or LLM invocation.

Failure rule: isolate the failed dataset/card, retain last authorized good rows and their true timestamp, show the safe cause, and retry boundedly. Never replace an unavailable measurement with zero or break another dashboard card.

### B. MCP polling

Main proposes an existing owner connection, exactly one read-only tool and nonsecret validated arguments, interval, row mapping and keys. The visible editable binding is stored server-side and referenced by opaque ID. Connection ID is not a transferable credential. Authentication uses only the existing connection; secret arguments are refused.

A binding example in descriptive notation: connection `market`, tool `intraday_quotes`, arguments `{symbol: NVDA, session: regular}`, interval 60 seconds; mapping `rowsPath=/quotes`, fields `time <- /timestamp` as timestamp, `value <- /close` as finite number, `series <- constant NVDA`; key `time,series`. Mapping grammar permits bounded object/array paths, field rename, constants, declared scalar conversions, null filtering, and bounded timestamp conversion with explicit timezone. No JavaScript, expressions, regex execution, shell, dynamic tool names, URLs, joins, or unbounded recursion. Invalid rows reject the sample atomically with a schema cause; do not partially present a misleading market series. Histograms use provider bins or a named built-in server aggregation, separately allow-listed.

The scheduler runs with the owner's session-less authority described above. Require all three at execution: the MCP server marks the tool read-only, its current effective permission is **allow**, and its definition matches the approved canonical hash. A `propose` tool is ineligible; approving the binding does not convert its tool permission. Reuse [MCP pin re-approval](mcp-tool-changes.md): mismatch pauses polling, invalidates the grant, and requires re-approval of the definition and binding revision; do not silently auto-accept a poller's changed tool. Read-only metadata changes invalidate the binding even if the existing definition hash omits annotations; store and recheck that requirement separately.

Every agent-created or agent-modified binding is a proposal. Main uses the existing person-required ask flow to approve the exact connection, tool/hash, arguments, mapping, interval, visibility and expiry **once per revision**, then Core stores the bound grant. Chat assertions or worker responses are not approval. Later scheduled calls execute under that approval without new asks; any material edit needs a fresh approved revision. A denied/canceled/stale proposal never activates. Side-effecting tools are excluded.

MCP-backed cards render **only for the binding owner** by default. Other viewers of a shared dashboard see `private to <owner>` and receive no rows, thumbnails, tooltip values or payload-bearing exports. Sharing requires an explicit person-approved viewer grant to that binding, subject to provider/connection policy; dashboard sharing alone grants nothing. Shared viewers never receive connection credentials. Withdrawal purges their derived content immediately.

Use durable due times, leases and numeric quotas above. Hard interval floor is 15 seconds; default 60 seconds; effective interval is the maximum of request, installation floor and provider minimum. No overlapping call or catch-up bursts. Settings offers pause/resume, expiry and next attempt.
Failure rule: last good authorized rows remain, labeled delayed/failed with last tick and last successful fetch; retry transient errors, pause on revoked authority or invalid wiring. A scheduler failure never launches an agent to repair itself.

### C. Live files

Preserve existing artifact identities and `publish --live`. Files can supply rows to a dashboard card or a full chart/dashboard source. Republish is atomic: parse, validate, authorize, and compare structural identity before acceptance. For an unchanged configuration, diff rows by key and deliver the same changeset as other sources. A structural edit creates a staged document update, not a fake data delta. A regular republish still follows version semantics; pinning an older version disables following.

No backend arbitrary-path watcher or execution of a publisher script. The authorized publisher owns refresh; Blerg exposes disconnected/stale state without pretending the producer is still running. A file reference cannot escape artifact visibility, resolve filesystem paths, or fetch external URLs. A dashboard can reference a separate rows artifact; no spec carries publish credentials.

Failure rule: retain last valid authorized revision/sample, surface missing file, refused content, or stale producer locally, and log the cause. A bad publish never replaces a good chart with raw JSON or a blank surface.

### Failure matrix and common policy

Every failure path emits a safe structured cause code through shared diagnostics, including renderer, mapping, scheduler, transport, permission, validation, export, and resource-budget failures. Do not log raw rows, specs, arguments, provider bodies/errors, credentials, private paths, or URLs. Use bounded opaque IDs, stage, status, sequence and suppressed repeat counts. A browser-only failure needs a bounded authenticated diagnostic route; existing server middleware alone cannot see it. Test all paths with a canary secret. UI copy uses safe categories with a diagnostic ID, never a raw exception.

| Condition | Blerg datasets | MCP polling | Live files |
|---|---|---|---|
| Minimum cadence and quotas | Event coalescing at most once per second per source; machine sampling floor 5 seconds, default 15; bounded recomputation and fan-out. | Hard floor 15 seconds, default 60; enforce provider minimum and shared quota too. | Accept publication within existing quotas; coalesce data delivery to at most once per second, retaining latest sequence; reject excess publish load explicitly, never create an unbounded queue. |
| Rate limit / transient failure | Keep snapshot; retry recomputation at 1, 2, 4 seconds up to 60 seconds with jitter. | Honor `Retry-After`; otherwise exponential backoff starting at effective interval, capped at 15 minutes with jitter; provider delay may exceed cap. Reset after success; never overlap. | Return safe quota/retry metadata to publisher; Blerg cannot retry publication on its behalf. Socket recovery uses snapshot handshake and backoff. |
| Stale / expected quiet | Default stale after two promised sampling periods; event datasets carry watermark/heartbeat so no events is not mistaken for failure. Show coverage gaps. | Separate event time, successful fetch time, next attempt and provider delay. Stale after two expected intervals plus declared provider delay. Provider-reported session status distinguishes market closed/halt from outage; absent status is unknown, not inferred from a local calendar. | Producer declares expected cadence (floor 1 second); stale after two periods. If absent, show last updated and “freshness unknown,” never claim live. |
| Provider unavailable / invalid sample | Only affected sources fail; retain good values, indicate unavailable producer or schema problem. | Timeout/5xx retries; malformed mapping/schema pauses after a named validation error; good values retain their old time. 401/403 pauses for reconnect/reapproval. | Deleted/gone source shows missing; invalid source keeps good state with refusal reason and source access. |
| Permissions / visibility | Apply record-level visibility before aggregation; no hidden totals, labels, or cross-user cache leakage. | Owner-only; others see “private to <owner>.” Explicit binding viewer grant plus provider/connection policy is required to share; dashboard ACL is insufficient. | Resolve artifact ACL on each read/delivery; dashboard visibility cannot elevate a referenced file. |
| Permission revoked while visible | Immediately purge affected rows, tooltips, retained images, exports and caches; show access removed. Do not retain last good data. | Same purge; cancel leased/in-flight calls and discard late results using connection generation. | Same purge; invalidate derived previews and subscriptions. |
| Secrets / spec safety | Dataset names/parameters are allow-listed, never SQL or agent tokens. | Only opaque binding IDs in documents; no credentials or secret arguments; no executable mapping. | No paths, credentials or URL loaders; same visual/file validation gates. |
| Empty / null / nonfinite / wrong unit | Valid empty set shows no observations; null is unknown; nonfinite/schema/unit mismatch rejects sample, never becomes zero. | Same; keep last valid sample with schema failure and truthful timestamps. | Same validation before accepting replacement. |
| Timeout / cancellation / late result | Bound recomputation deadline; cancel on revoked scope; ignore old generation. | 30-second deadline; fence result on pause/revocation/revision; retry only while eligible and visible. | Abort superseded reads; late file decode cannot replace newer valid revision. |
| Socket loss / reconnect / slow viewer | Mark disconnected; cap outgoing queue and replace backlog with snapshot-required; reauthorize and resume by epoch/cursor. | Same; viewer heartbeat expiry pauses calls, never builds a hidden backlog. | Same; producer updates and browser delivery remain separate states. |
| Schema/config edit / optimistic conflict | Validate catalog revision; incompatible change pauses affected card, source remains reachable. | Hash/mapping mismatch pauses; stale edit revision is rejected; no half-applied binding. | Stage structural edits atomically; rejected/conflicting replacement preserves old authorized scene. |
| Restart / duplicate dispatch | Replay Board cursor and Git SHAs idempotently; rebuild on gap; audit interrupted refresh. | Durable leases/fences; no catch-up burst, fresh Core capability, interrupted-attempt audit. | Reconnect by artifact identity/sequence, never republish automatically. |
| No viewers / hidden preview | Drop delivery/sampling demand; retain aggregate checkpoint. | Lease expires within 60 seconds; pause, no calls; 7-day auto-pause and 30-day approval expiry. | Unsubscribe preview; external publisher lifetime is not controlled by viewing. |
| Quota / queue / oversized rows | Bounded compute queue; show delayed/overload; keep authorized prior snapshot. | Numeric owner/connection/pool caps apply; back off on overload; reject oversized sample atomically. | Enforce document/row/publish caps; keep last valid revision. |
| Credential / audit store unavailable | Fail closed before refresh; log safe cause; resume only after authority/audit recovers. | Do not dispatch; show paused authority/audit unavailable; no fallback session token. | Publication uses existing authority; denied publish leaves prior valid file. |
| Tool pin / proposal / binding expiry | Not applicable to record-derived queries; schema changes fail closed. | `propose`, non-read-only, hash mismatch, an unapproved revision never runs, and expiry prevents calls; a still-valid old approved revision may continue during an ordinary edit proposal; precise paused reason, existing ask for eligible re-approval. | Refused file content never bypasses visual policy or activates a binding. |
| Reorder / duplicate / sequence gap | Deduplicate event IDs; request checkpointed rebuild. | Deduplicate keyed rows; reconcile full authorized snapshot on gap; discard stale generation. | Diff only against matching base; fetch latest valid snapshot after gap. |
| Renderer / unsupported live / theme override | Isolate card; native-live only, otherwise `unsupported-live` with static snapshot; reject theme overrides before paint. | Same; do not poll solely for unsupported live card. | Reject offending replacement; source one click away; last good authorized scene stays. |
| Export failure / deletion / TTL | Local export error cannot break live cards; source record deletion recomputes/removes rows. | Current-row export only after viewer check; TTL/deletion purges rows and derived images; safe unavailable state. | Export fails locally; deleted artifact shows missing and follows existing retention policy. |


Socket outage applies to all: last-known data may remain while current local authority is valid, clearly disconnected; reconnect reauthorizes before accepting data. Already downloaded exports cannot be recalled; sharing UI must state that boundary. Ordinary stale/provider failure preserves data, authority loss does not. Provider-reported market closed is an availability fact, not evidence that a failing provider is healthy: show both when applicable.

## Iteration through Main

“Make the merges card daily and compare the previous release” identifies the card by title/ID, previews a minimal source change, validates it, and republishes the same file identity. Preserve current filters, pane and focus wherever their semantics survive. Main explains any ambiguous baseline or missing source rather than silently inventing numbers. Live wiring is visible from Source and editable in the existing settings panel; a generated spec is never a hidden agent memory dependency.

### Main wiring interface

Add matching CLI and Runner MCP operations, with shared validation and structured errors (proposed names, not available commands today):

| CLI | MCP tool | Contract |
|---|---|---|
| `blerg-runner datasets list --json` | `live_datasets_list` | Authorized catalog with schema, units, parameters, limits and examples; never hidden datasets. |
| `blerg-runner binding create --file binding.json` | `live_binding_create` | Validate connection/tool/hash/args/mapping/interval; return pending proposal ID/revision and bound existing ask, not an active binding. |
| `blerg-runner binding update ID --if-match REV --file binding.json` | `live_binding_update` | Optimistic revision; material edit proposes replacement and preserves approved old revision until replacement approval, unless unsafe/revoked. |
| `blerg-runner binding get ID` / `binding list` | `live_binding_get` / `live_bindings_list` | Owner-visible state, safe configuration, expiry, pause reason, next attempt and quota, no credentials. |
| `blerg-runner publish dashboard.dash.json` (or `--live`) | `publish_file` with dashboard type | Existing artifact publication contract extended for validated `.dash.json`, revision/base conflict, preview and tab routing; never implicitly approve referenced bindings. |

Bind approval to the existing ask's exact proposal ID/revision and verify its server receipt. CLI and MCP callers have identical permission/proposal behavior. Include pause/delete owner operations in existing settings, with API contract tests. Update the **visualizing skill** and worked templates in the delivery track: four dashboard card kinds, template forms, strict color refusal, dataset discovery, binding proposal/approval, native-only live support, visibility-based polling, same-name publication, safe failures and one-card edits. This phase specifies that update; it does not modify the skill or ship commands.

Ship small templates for time series, comparisons, breakdowns, distributions, stat with sparkline, gauge, heatmap, status timeline, histogram, text, and composed dashboards. Templates declare required schema, units, keys, domain policy, freshness, empty state, accessibility summary, and supported renderer. Start from question and finding, not the shape of raw data. Source is one click away for good, refused, stale and historical views. Reuse existing open-tab, source, download, review, and toolbar patterns; no repeated headings or permanent refresh banners.

## Chart design standard and quality gate

Titles state a finding when evidence supports one; a neutral question is preferable to an invented claim. Use sentence case and American English. Axes carry units and timezone, bars normally include zero, truncated numeric line domains are explicit, categories sort by value unless order is meaningful, and legends name series once. Preserve event markers and detailed tables under shared filters; totals must not collide with annotations. Use exact values on inspection, sensible tick precision, and accessible text contrast. Stable series colors come from tokens; use line style, pattern, shape and labels for additional distinctions. No decorative gradients, unexplained gauges, chart junk, or rainbow dashboards. Card alignment and restrained motion provide the joy.

The rendering suite is a release gate, not just a screenshot gallery:

- Run **every shipped template** at pane widths **390, 520, 1024 and 3840 px**, in the default and a color-blind theme, including narrow panes inside a wide window. Exercise chat preview, full tab and dashboard embedding with fixed clocks, seeded data and font readiness. Use the real-app harness patterned after `PopOutMessage.browser.test.tsx` or `InsightsApp.browser.test.tsx`, not a standalone renderer page.
- Cover zero, missing, single point, long titles/labels, large values, negative values, multiple layers/independent axes/shared colors, dense times, new/removed series, filtered event annotations, and composition fallback. Compare supported geometry/values/legends against Vega through the existing differential seam; never relax policy for a fixture.
- Frame-sample a **representative set**, not every template × width × theme: a layered line/rule at 390 px, a many-series bar at 520 px in a color-blind theme, and a mixed stat/gauge/chart dashboard at 1024 and 3840 px. For that set sample **every committed frame** from shell mount to first draw and across continuous splitter drag, tab transfer, font load, resize, theme switch and data arrival. Record measured box, generation, plot bounds, visible scene identity and blank pixels at a pre-paint test hook, paired with browser screenshot frames. Assert no revealed provisional/wrong-size scene, no blank after first success, no overflow, and no transient loss of legend or labels. A final screenshot alone cannot pass this gate. Retained fallback snapshots are explicitly identified and fitted; test exact final geometry after drag settles.
- On data-only changes, assert chart/axis/legend DOM identity, keyed mark retention, no recompilation, correct new/changed/removed marks, preserved scroll and hover, correct export snapshot and sequence recovery. Sample intermediate motion geometry under a controlled animation clock. Under reduced motion assert immediate final geometry and zero animation frames scheduled for motion.
- Hit-test different marks and empty space, pointer leave, touch pin/dismiss, keyboard traversal, Escape propagation, tooltip pane edges and live deletion of a hovered key. Verify screen-reader summaries, table access, focus visibility and non-color differentiation.
- Contract tests use fake time for scheduler floor, quota, backoff, leases, cancellation, mapping limits, provider session status, seq gaps, schema drift, approval pause and every failure-matrix row. Cross-service tests use provider-owned canned MCP fakes; permission guard mutation must fail tests, with canary secrets proving diagnostic redaction. Real provider staging is separate and honestly reported.
- Initial interaction budgets: native first ready scene within 300 ms and data/resize commit within one 60 Hz frame for bounded template fixtures after dependencies/fonts are ready; record frame and memory distributions for 32 cards, burst updates and 4K. Worker fallback has a separate measured readiness budget bounded by existing deadlines, and may never blank while pending. A missed budget blocks its delivery card until scope/algorithm is reviewed; do not silently increase limits.

For implementation review, save real-app screenshots at 390 px, in a 1024 px pane, and at 3840 px under a Vite-allowed path, verify encoded dimensions, publish, and inspect every image. Include the 520 px and color-blind suite evidence as well. `make lint-theme` must remain at zero violations. Phase 1 changes no UI, so screenshots, frontend tests, type checks and builds are not evidence for this note and are intentionally not run here.

## Reference demos

The existing throwaway scripts were read as examples, not executed or adopted as production schedulers.

### Intraday NVDA

`nvda_poller.py` polls Yahoo's unofficial chart endpoint at one-minute cadence in weekday regular hours, fifteen minutes otherwise, republishes a full layered spec, and uses a weekday/time check rather than an exchange holiday calendar. Its value is the growing intraday line and previous-close comparison. Its endpoint, full republish loop, raw exception printing, and local time approximation are not production contracts. Building a trading calendar is outside this epic; missing provider session metadata is shown as unknown.

Acceptance demo: wire a read-only market MCP through the binding above, with a provider-authorized real feed or deterministic stub. Label provider, exchange timezone, delayed entitlement and currency. Use a regular-session trading-day axis, with timezone/DST fixtures and provider-reported session bounds/status; do not bridge missing intervals as uninterrupted trading. Show the previous-close baseline with distinct token-derived series treatment and exact price/time tooltip. Append or correct minute samples by key without remount, preserve all bounded session points, then roll the next session explicitly. Show latest tick, last successful fetch, market closed, stale feed, halt, 429/backoff, revoked connection and provider outage truthfully, including combinations. A stub must be visibly labeled simulated. No claim of real-time entitlement without provider evidence. No market source credentials or network requests belong in the chart spec.

### Blerg live telemetry

`dashboard_poller.py` shells out for usage/session state, reads local load/disk and merge history, and publishes a concat of floating text tiles and plots every minute. It can silently turn failed reads into empty strings; it also names a specific machine capacity/disk. Keep the questions it answers, replace those assumptions with authorized named datasets and honest unknowns.

Acceptance dashboard: stat cards for merged today with previous-period delta and active/waiting workers; gauges for engine weekly usage with reset time, load with CPU capacity, and disk free with labeled threshold; span-2 trends for gates/load; span-4 merges by release; heatmap of merges by hour/day; worker status timeline; gate-duration histogram; a text card explaining observation coverage. Separate each card's source/freshness. Drive recorded card completion/merge, gate, review and telemetry events and prove only affected marks update. Simulate daemon disconnect, missing usage, disk unavailable, hidden board records, engine reset and invalid data without zero substitution. At 4K use the full grid; on a phone keep values and labels readable in one column. This demonstrates the Blerg dataset pipeline through all four card kinds and the heatmap/timeline/histogram chart templates.

## Delivery in 0.6.0

Proposed independently reviewable slices, **not newly filed cards**. Estimates are engineering days including targeted tests and review, for one worker familiar with the region; they are not elapsed dates or an approved staffing plan. Size, risk and complexity are candidate first-class card fields. Search/reconcile existing cards before filing; preserve their owners. Claude reviews this note first, then Main files/links work. The release gate runs once on the integrated tree; individual workers run touched regions only.

S1 is the first visible increment, in **3–4 working days after design approval**: `.dash.json` grid in the existing viewer/preview, stat with sparkline, gauge, text and chart from inline rows, strict token validation and responsive screenshots. It is explicitly static; it does not pretend string-built charts support live updates. S2 follows: first named datasets, live stat and gauge, authority/freshness/visibility and restart evidence. Sparkline updates remain static/unsupported-live until B/C; the live scalar components update their mounted text/indicator directly. These are internal reviewed increments, not permission to cut the final 0.6.0 scope.

| Slice | Deliverable and independently reviewable boundary | Dependencies | Days | Size / risk / complexity |
|---|---|---|---|---|
| A1 / S1 | Schema plus frontend: four inline card kinds, responsive grid, preview/tab/source/version path, strict theme validator and ramps, basic accessible static export; visible first increment | Design approval; BLG-506/507 behavior | 3–4 | M / medium / medium |
| A2 | Protocol plus authorization: Core session-less grants, Board event feed/cursors, Runner source/sequence contract, audit/lease/retention schema and contract fakes | Design approval; coordinate A1 format | 5–7 | L / high / high |
| S2 | First datasets: done per release, deterministic Git merges, usage/load/disk for live stat/gauge; visible-only subscriptions, freshness, restart and purge evidence | A1, A2 | 4–6 | L / high / high |
| B | Retained-mode `blerg/svg.ts` rewrite: keyed scene, measured layout, persistent axes/legend, atomic resize/theme commits and current-scene export; retire BLG-507 interim heuristics | S2; A2 contract | 7–9 | L / high / high |
| C | Retained-mode reconciliation: keyed changesets, domains, layered native live support, hover/focus, entrance/update/reduced motion; reconcile BLG-483/485 | B | 7–9 | L / high / high |
| T | Native chart templates: heatmap, histogram, status timeline plus other catalog forms; accessibility, eight-plus-series styles and export parity | C, A1 | 4–6 | L / medium / high |
| G | Remaining datasets: sanitized gate report ingestion, gate durations, review engine pairs, card time, heatmap/timeline/bin aggregates and correction semantics | S2, A2 | 3–5 | M / high / medium |
| I | MCP binding CLI/MCP/settings, nonsecret mapper, server read-only + allow + hash-pin admission, person-approved revisions and explicit sharing grants | A2, S2 | 4–6 | L / high / high |
| J | Durable poller: Core/gateway capabilities, audits, pool/quotas/leases, no-view pause, expiry/TTL/purge, backoff and native-live admission | I, A2; C before live charts | 5–7 | L / high / high |
| K | Live-file native changesets, stage config changes, validation and freshness; static snapshot for unsupported live | C, A1 | 2–3 | M / medium / medium |
| L | Main one-card edits, optimistic revisions, template examples and visualizing skill update | T, I, K | 2–3 | M / medium / medium |
| M | Representative frame-sampling harness, full static width/theme matrix, differential/privacy/a11y/failure fixtures; feature cards carry their own tests | A1 starts harness; B/C define scene hooks | 4–6 | L / medium / high |
| N | Both acceptance demos, real-app screenshots and integrated release evidence; no live Vega work | T, G, J, K, L, M | 3–4 | M / medium / medium |

Total: **53–75 engineering days**, including targeted tests and review within each slice; B+C alone are **14–18 days**. Allow **15–20% integration/review contingency**, giving roughly **61–90 engineering days**. No slice D: static Vega fallback remains unchanged except shared policy enforcement.

### Four-worker schedule

This is a feasible staffing model, not a claim that effort divides evenly by four. Work in serial renderer files stays with one worker; do not create conflicting rewrite branches. Reviews happen at each boundary, and blocking findings shift downstream dates.

| Track | Sequence and approximate working-day windows after approval |
|---|---|
| Worker 1, frontend/renderer | A1/S1 days 1–4; S2 frontend days 6–13 with Worker 2; B days 14–22; C days 23–31; assist template integration/review days 32–37. |
| Worker 2, source services | A2 days 1–7; S2 service integration days 8–13; G days 14–18; assist I/J authority and Core/Board review days 19–30; recovery/retention integration days 31–37. |
| Worker 3, bindings/templates | Early A2 mapping/admission contract review; I days 14–19 after S2; J days 20–26 (native-live integration waits for C); T days 32–37. |
| Worker 4, proof/integration | M harness days 2–7 with A1 fixtures; add representative B/C frame cases as hooks land, review failure/security evidence; K days 32–34; L days 38–40 with Worker 3; coordinate N days 41–44. |

Windows include dependency/review slack; idle slots are not filled by expanding scope. The first dashboard is reviewable in 3–4 days and first live scalars around days 10–13. Renderer/live-chart critical path reaches templates around day 32; final demonstrations and integrated gate target **8–9 working weeks**, with **9–11 weeks** realistic after contingency and review fixes. Four available workers do not make this a two-week epic. Keep all work in 0.6.0; slip the release date if proof or integration requires it. Never silently replace native changesets with full refreshes or weaken the theme/authority rules.

Phase 1 ends with this committed, published note and the review handoff. Implementation, card creation, release rescheduling, and review approval are outside this worker's current scope.
