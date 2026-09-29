export type ArrowDir = 'up' | 'down' | 'left' | 'right'

const FINAL: Record<ArrowDir, string> = { up: 'A', down: 'B', right: 'C', left: 'D' }

// Cursor keys differ by mode: a TUI in DECCKM (application cursor keys) expects
// SS3 (ESC O), otherwise CSI (ESC [). Sending the wrong form causes menus to ignore the key.
export function arrowSeq(dir: ArrowDir, appCursorMode: boolean): string {
  return (appCursorMode ? '\x1bO' : '\x1b[') + FINAL[dir]
}

export const KEY_SEQ = { enter: '\r', esc: '\x1b', tab: '\t' } as const
