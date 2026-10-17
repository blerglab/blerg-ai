# Integrating the 0.3 engine work

Status: the owner confirmed 0.2.1 has landed. Its release commit `818e7c1` and the subsequent
PDF-observer test fix at `eb72ba2` are integrated into the isolated `feat/engine-parity-0.3`
branch by merge `0923df5`. Main and the release checkout were not modified. This is source
integration and automated fixture validation; it is not deployment or live-provider validation.

Use the [implementation audit](engine-parity-review.md), [feature inventory](../features.md),
[Adding an engine](../adding-an-engine.md) and [contribution checks](../../CONTRIBUTING.md)
alongside this checklist.

## Integrate in the isolated worktree

1. Record the confirmed 0.2.1 commit and the clean 0.3 branch head. Merge the confirmed commit
   into the isolated 0.3 branch. Keep the release session's checkout and index untouched.
2. Resolve conflicts around behavior, preserving both sides' fixes. Review these boundaries:

   | Boundary | Behavior to retain |
   | --- | --- |
   | Account capacity | Keep the 0.2.1 Claude spend reader and meter UI. Keep 0.3's adapter-declared `UsageWindows`; Claude allows `month`, `day` and `spend-week`, while Codex keeps the windows its source actually reports. Do not restore a shared hardcoded provider/window list. |
   | Capacity privacy | Reports contain provider utilization and known reset times. Preserve fractional percentages and unknown billing resets. Do not add raw monetary totals, infer balances from session token costs, or substitute another account's capacity. |
   | Native events | Keep configured child effort, explicit reset handling and chronological model/effort pairs across background launch acknowledgments, updates and completion. Keep unknown values when native metadata is absent. |
   | Usage and prices | Keep complete bounded model partitions, aggregate validation, unknown attribution and one logical turn count. Missing token buckets remain zero for compatible payloads; present invalid buckets cannot acquire prices. Preserve native turn fencing and child resume baselines. |
   | Tool definitions | Keep actual-adapter `tool_schema_refresh`, unknown legacy behavior and owner-scoped latest-report lookup. Approval changes authorization; it does not prove a native schema reload. |
   | Credential authority | Preserve fresh owner/runtime authority, engine binding, private runtime tokens and restricted-session confinement. A model catalog or capacity report never authorizes execution. |
   | Discovery | Keep principal/target-scoped cached launch discovery, explicit pending/error/empty states, compatible host selection and immutable existing-session engine identity. Copy runtime/daemon_id from the selected engine option, not an ambiguous top-level automatic target. |
   | Schema and release metadata | Check migration numbering against 0.2.1 and retain already-released migration identities. Keep the release owner's version/changelog changes; put new behavior under the intended unreleased section. |

3. Review the resulting diff and migration order before running the checks below. A clean
   textual merge alone is insufficient evidence of compatibility.

## Verify the merged source

Use dedicated test databases, sanitized environments and synthetic native executables, as in
[CONTRIBUTING.md](../../CONTRIBUTING.md). Database suites from different modules or workers
must not share a database. Use the toolchain and linter version pinned by the integrated source.

- Run the affected Go race suites: shared descriptors, daemon/native adapters, agent events,
  Live state, protocol, DB Insights, server controls/discovery/capacity/tool-change authorization,
  and MCP transports. Include existing compatibility fixtures, not just the new tests.
- Run shared Chat, runner and Board type checks, lint, unit/browser tests and production builds.
  Check the actual embedded consumer separately when its integration is being merged.
- Run CI-pinned Go lint, dependency checks, secret scrub and documentation links. Record skipped
  checks and actual results; the earlier branch's green results do not certify the merged source.
- Independently review conflict resolutions and the resulting authorization, privacy and native
  accounting behavior. Repeat a regression when its implementation changed during resolution.

Existing focused evidence is in the [audit's follow-up verification](engine-parity-review.md#reporting-and-capacity-follow-up-for-03).
The following matrix describes the acceptance scenarios. Automated fixtures cover the boundaries
recorded in the verification results; live workflows with actual provider accounts remain separate.

| Scenario | Expected result |
| --- | --- |
| Only one available engine | Its models appear without an engine selector; absent engines are excluded. |
| Multiple available engines | Engine selection refreshes the model/effort choices for the same principal and target. |
| No engine, pending probe, failed probe, known empty catalog | Distinct bounded states and retry/manual-entry behavior; no indefinite loading or borrowed catalog. |
| Host versus Docker versus Cluster | Discovery and launch use the same runtime and host; credentials and restrictions are revalidated on launch. |
| Human, account-bound agent and operator callers | Catalog, host and credential scopes remain separate; account-only cron discovery excludes operator credentials. |
| REST/MCP headless launch | Discover available options first, forward engine/model/effort and runtime/daemon_id from the selected engine option, preserve idempotency and receipt behavior. |
| Existing embedded chat | Native engine identity is preserved; model controls use the actual session target; engine changes require an explicit new native session. |
| Claude spend limits alongside subscription windows | Both render when reported, including utilization below 1 percent and caps without a known reset. Codex's reported windows continue to render independently. |
| Account capacity versus session Insights | Capacity reflects the reporting account's source; estimated session costs and unknown model usage cannot manufacture a remaining monetary budget. |
| Native root/child model switch | Known sampled usage remains attributed, uncertain usage stays unknown, totals are counted once, and delayed events carrying old native turn IDs do not contaminate a resumed run. |
| Child effort changes, reset and recovery | Chat, Live and Insights agree; omitted partial metadata preserves the latest observation, an explicit reset clears it, and no parent effort is inferred. |
| Accepted tool change | Reported refresh policy gives accurate next-turn/new-session guidance. Removed or unauthorized calls stay denied even when an old native schema remains loaded. |

After the release owner has made an integrated build available, run representative interactive,
headless and embedded workflows with the intended test accounts. Record what was actually
observed for each engine/runtime. Starting live sessions or changing a deployment is a separate
step from preparing and testing this branch.


## Confirmed integration record

The confirmed release is `818e7c1`; the merged upstream target is `eb72ba2`, which also includes
the PDF observer test fix. Merge `0923df5` combines that source with the prior isolated 0.3 head
`11935eb`. The 0.2.1 release history, `VERSION` and shared Chat package version were retained.
Conflict review preserved both branches' controls, durable messages, artifact navigation,
file-limit metadata, capacity windows and current runtime authority.

Independent merged-source review found no blocker in credential binding, native confinement,
principal/session-scoped authorization, proxy forwarding or capacity privacy. A new Board fixture verifies that
explicit false steering and empty effort values survive row fallback without dropping file limits.
The full validation results are recorded in the [audit](engine-parity-review.md).

Live provider workflows and deployment remain separate acceptance steps. This integration did
not replace binaries, restart the daemon, change a deployment, modify main or bump a version.
