import type { CSSProperties } from 'react'
// Stable server keys; theme variables resolve the paint at display time.
export const SESSION_COLORS = [
  { key: '', name: 'None' },
  { key: 'gold', name: 'Gold' },
  { key: 'rose', name: 'Rose' },
  { key: 'blue', name: 'Blue' },
  { key: 'lime', name: 'Lime' },
  { key: 'purple', name: 'Purple' },
  { key: 'orange', name: 'Orange' },
  { key: 'silver', name: 'Silver' },
  { key: 'violet', name: 'Violet' },
  { key: 'teal', name: 'Teal' },
  { key: 'indigo', name: 'Indigo' },
  { key: 'coral', name: 'Coral' },
  { key: 'cyan', name: 'Cyan' },
] as const
export function sessionColor(value?: string) { return SESSION_COLORS.find(c => c.key === value) ?? SESSION_COLORS[0] }

export function sessionColorStyle(color?: string): CSSProperties {
  const item = sessionColor(color)
  return { '--workspace-color': item.key ? `var(--session-color-${item.key})` : undefined } as CSSProperties
}
