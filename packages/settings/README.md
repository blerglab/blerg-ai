# Shared settings primitives

Core, runner and board use the same CSS primitives, with their existing brand tokens.
Import `settings.css` with the settings screen. App-specific classes can coexist.

- Section: `settings-section`, with a `settings-heading` and a one-sentence
  `settings-description` explaining what the person can change.
- Toggle: `label.settings-field` containing label text and an
  `input.settings-toggle-control` with `type="checkbox"` (or radio).
  For an immediate-save switch use `settings-toggle` around label text and
  `button.settings-toggle-control` with `role="switch"`, `aria-checked`, and
  an accessible name. Keep disabled state and error feedback.
- Field: `settings-field` on a label and its input, textarea or select.
- Action: `settings-action` on a button; add `primary` for the main action.

Native controls retain keyboard and form behavior. The toggle keeps its label
left and switch right without inheriting full-width input styles. Your desktops
(core Settings → Account) uses the same primitives and settings guard.

Each app's SettingsPrimitives test follows local component imports from its settings
screens, automatically covering new sections, conditional controls, and error actions, and reports bare buttons, dropdowns, checkboxes and radios. The
browser tests check actual styling and layout at 390 and 3840 pixels; set
`VITE_SETTINGS_SHOTS=<absolute checkout path>/.cache/settings-shots` to save runner and board screenshots.
Run `cd core/web && SETTINGS_SHOTS=/tmp/settings-shots node tests/settings.mjs`
for core screenshots, including close-ups of Owner shells and Release notices.

## Button guard

`buttons.ts` checks every `<button>`, `<input type="button|submit|reset">` and `role="button"`
element in an app against the CSS the app loads. A control must provably get a real background
and border at rest: from a design-system class (`btn`, `icon-btn`, `settings-action`,
`artifact-btn`, `link-btn`, as the app has them), a component class or container rule whose
whole selector matches it (no `@media`, pseudo-class or attribute conditions), or an inline style
with known values. Unresolvable spreads, classes and values fail closed. Each app's
`SettingsPrimitives.test.ts` (and `packages/chat/src/ButtonGuard.test.ts`) runs it; a reviewed
exception is `// button-guard-ignore: <reason>` (or the `{/* … */}` JSX form) alone on the line
above. See `CONTRIBUTING.md`.
