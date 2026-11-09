# UX audit outside sessions

BLG-421 covers the launch sheet, Runner settings and sidebar, Insights, Board header
and settings, and Core home and sign-in. Session contents are outside this change.

## Evidence and limits

The browser checks navigate the shipped App components through their normal routes
and controls in Chromium at 390 and 3840 px, with the default dark palette. Core
loads its real HTML entry point. Deterministic service responses populate the pages;
these checks prove frontend behavior, not a running installation's authentication,
permissions, transport, or data integration. Live-install acceptance remains outstanding.
Screenshots are session artifacts rather than repository assets.

## Decisions

- Board header: retain all destinations and the shared ThemePicker. Place the picker
  beside the brand and version on phones, and navigation on its own row. Compact
  spacing preserves touch targets and the space available below the header.
- Release planning: keep its existing controls and native fields. Intrinsic field
  widths let the Release label sit beside its choice instead of wrapping above it.
- Board settings: reuse the existing bounded stack form for editable fields and put
  Save key in the existing action row, matching Save general and Save repos. Fields
  no longer stretch across a 3840 px display.
- Runner phone navigation: retain every destination and use the existing horizontally
  scrolling navigation pattern. Each label keeps its natural width; Settings and
  Insights can be reached without widening the page.
- Runner settings: give Default verbosity the same natural-width treatment as the
  scope picker. Scope the rule to Settings so session verbosity is unaffected.
- Launch sheet: keep its existing close control; give it the same accessible Close
  name as Settings and a tooltip.
- Insights, Core home and sign-in: retain the existing components and visual patterns.
  No additional visual change was justified by the recorded views.

## Reproduction

From the workspace root:

```
npm test -w board/web -- --project browser src/OutsideSessionAudit.browser.test.tsx --maxWorkers=2
npm test -w runner/frontend -- --project browser src/components/OutsideSessionAudit.browser.test.tsx --maxWorkers=2
```

Set `VITE_AUDIT_SHOTS_DIR=.cache/audit` to record browser-test screenshots (relative
to each test file). From `core/web`, run `node tests/outside-session-audit.mjs`;
`AUDIT_SHOTS` optionally names an output directory.

The regression checks cover the release field's row, the key button and verbosity
field widths, phone navigation into Settings and Insights, and the accessible launch
close action. Existing mobile Board tests also constrain the session composer's space.
