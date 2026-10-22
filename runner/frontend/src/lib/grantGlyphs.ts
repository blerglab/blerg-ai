// One glyph per kind of permission, the same wherever that kind shows: the Live tab's permission
// chips, the Permissions dialog (launch and running), and the child-session approval in Needs you.
// ↳ is reserved for nesting under Main (Running now), so no grant kind may use it; a new kind gets
// its own glyph here. The words stay in titles and aria-labels.
export const GRANT_GLYPH = {
  /** Can start sessions: a fork. */
  child: '⑂',
  board: '▦',
  tool: '⚙',
} as const

export type GrantKind = keyof typeof GRANT_GLYPH
