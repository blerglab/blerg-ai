// BLG-485: a diagram node/participant/task label or a chart axis tick/legend entry is one
// candidate string, not prose — so this checks the whole, trimmed label against the chat's own
// reference parser (model/refs.ts's `splitRefs`, which already recognizes a full `KEY-N`,
// `board:ID#N`, a bare `#N` and a commit SHA) rather than re-deriving a parallel pattern. `null`:
// the label isn't (only) one reference — stays a plain label, no chip, no lookup. The returned
// key is the exact `RefKey` shape refs.ts already uses, so the render step resolves it through
// the same `ChatRefs`/`RefContext` store as chat text, not a second one.
import { splitRefs } from '../model/refs'
import type { RefKey } from '../model/refs'

export function labelRefKey(label: string): RefKey | null {
  const trimmed = label.trim()
  if (!trimmed) return null
  const pieces = splitRefs(trimmed)
  if (pieces.length !== 1) return null
  const piece = pieces[0]
  return 'key' in piece && piece.text === trimmed ? piece.key : null
}
