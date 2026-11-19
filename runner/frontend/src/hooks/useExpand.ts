import { useId, useState } from 'react'

// BLG-466: a long card list shows its top N with a quiet control that expands it in place,
// instead of a fixed-height inner scrollbar (the "card section with scrollbar" the owner
// flagged). One hook shared by every capped list in Insights.
//
// resetKey: when a list is scoped to a selection (e.g. BarChart's drill-down records for the
// selected bar), pass the selection so switching it collapses back to the top N instead of
// carrying a stale "expanded" over to the new, unrelated list.

export function useExpand(total: number, limit = 50, resetKey?: unknown) {
  const id = useId()
  const [state, setState] = useState({ key: resetKey, expanded: false })
  // Adjust during render (the documented React pattern for resetting state when a prop changes)
  // rather than an effect, so the first render after resetKey changes never shows a stale list.
  if (state.key !== resetKey) setState({ key: resetKey, expanded: false })
  return { id, shown: state.expanded ? total : Math.min(total, limit), expanded: state.expanded, toggle: () => setState(s => ({ ...s, expanded: !s.expanded })), hasMore: total > limit }
}
