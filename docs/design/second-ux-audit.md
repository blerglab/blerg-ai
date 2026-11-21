# Second UX audit — BLG-423

## Method and evidence

Audited the actual Board and Runner application entry points in Chromium, using
Core authentication and real local HTTP/WebSocket services backed by separate,
disposable PostgreSQL databases. No browser request interception or component
fixtures were used for the walkthrough. Default dark colors were selected through
the browser's dark preference. Every surface was visited at 390 × 1000 and
3840 × 1000 pixels. Screenshots wait for content and finite entry animations.

Synthetic records include four estimated release cards, thirty days of workspace
history, a disabled weekly cron, a pending tool proposal with long arguments,
a long question and recovery URL, and external and live Board ticket pins.
The Board side view reads the same Runner conversation through its real proxy.
No engine, cron or proposed external action was executed. The setup has no
connected execution daemon, automation identity or external MCP connection;
those unavailable states remain visible and are not acceptance of a live launch.

Before/after images and a self-contained comparison gallery are session files,
not repository assets. The Board baseline was rebuilt from the original production
files before taking its final baseline captures. Existing public UI controls and
routes lead to each view; the full workspace page also accepts its normal deep link.

## Surface decisions

| Surface and location | Observed behavior and decision |
| --- | --- |
| Board card detail, Board → card | Retain the Board's existing card sheet, estimates, release choice, description and trail. Long titles wrap and controls retain their natural widths. |
| Board side view and full workspace page, card → Workspace | Remove runner adapter name, shortened session ID and retry counter from the shared Conversation header. Retain its existing state, model label, discussion label and actions. No replacement header or chat renderer. |
| Proposals, main navigation → Pending | Show the action once using the existing `ProposalAction`; show title or subject once in the readable preview. Keep the exact request intact. Use the existing `ProposalExpiry` once; remove the elapsed-time component that called a future deadline “just now.” |
| Crons, main navigation → Crons → Edit | The existing enablement switch says Enabled/Disabled. Remove its duplicate active/disabled badge. Retain paused/expired warnings, renewal, history and the existing form. No scheduling behavior changes. |
| Board metrics, Board → Metrics | Fit the existing SVG bars to the sheet, as the release chart fits its container. Preserve axis text size and chart height, reduce date-label density with width, retain first/last dates and all bars, and recompute on resize. Thirty days previously required horizontal scrolling at both widths. |
| Release planning, Board → Releases → release | Reuse the Board input rule for name, date, search and milestone fields. These previously appeared as raw browser controls because the existing rule only matched explicit text/password types. Preserve natural widths and all release actions. |
| Shared ticket cards, conversation → pin → detail | Retain the shared ticket card/modal. Check the external ticket and the live Board card, including description, metadata, trail and existing header maximize action. No new ticket view. |
| Questions, conversation → Explorer → Questions | Retain the existing explorer row and shared question dialog. The long question is shown once; its link wraps and its answer controls remain reachable. |
| Needs you, conversation → Workspace panel → Live | Retain the same question row/dialog used by Questions. The shared proposal preview also benefits from the title fix. Long question and proposal content remain inside the panel. |

## Product questions left open

- Should a live Board pin open the canonical Board card directly, or retain its
  existing compact live-card dialog? The dialog omits the release and estimate
  controls present in Board; its existing Open in Board action reaches that page.
  The audit retains this navigation behavior pending a product choice.
- What should a repository in a card detail open: its forge page, repository graph,
  or workspace? Existing read-only repository chips were retained; no destination
  was invented.
- Should fixed command steps remain a JSON editor in the basic cron form or move
  behind an advanced disclosure? The existing editor and approval behavior remain.
- Should Board metrics offer an exact-value table or a date-range control for long
  histories? This change preserves the existing chart and fits its bars; it does
  not introduce a new analytics workflow.

## Regression proof

Focused tests first failed for duplicate cron state, duplicate proposal title,
duplicate/future expiry, chart overflow, raw release inputs and runner identifiers.
After fixing them, restoring the corrected Board behavior to its previous form
made the corresponding regressions fail again; fixed files were restored afterward.
The session verification report records final suite counts, commands, failures,
commit IDs and operational limitations. No push or merge is part of this audit.
