import { BACKGROUND_COLOR, displayLabel, type DisplayState } from '../lib/displayState'

// The session's state in words, as a small pill: the session header's and the panel's. The sidebar
// card shows a glyph instead (its room is short); the words there are its title.
const COLOR: Partial<Record<DisplayState, string>> = {
  waiting: 'var(--amber)',
  running: 'var(--lichen)',
  starting: 'var(--batch)',
  background: BACKGROUND_COLOR,
  error: 'var(--danger)',
}

export default function StatusPill({ state, tasks = 0, testId = 'status-pill' }: { state: DisplayState; tasks?: number; testId?: string }) {
  const color = COLOR[state] ?? 'var(--fog-dim)'
  return (
    <span
      data-testid={testId}
      data-status={state}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 4,
        color,
        border: `1px solid color-mix(in srgb, ${color} 40%, var(--stone))`,
        background: `color-mix(in srgb, ${color} 10%, var(--basalt))`,
        borderRadius: 10,
        padding: '0 6px',
        fontSize: 11,
        lineHeight: '16px',
        whiteSpace: 'nowrap',
        verticalAlign: 'baseline',
      }}
    >
      {state === 'background' && <span aria-hidden="true">⏱</span>}
      {displayLabel(state, tasks)}
    </span>
  )
}
