import { useEffect, useState } from 'react'

export interface VisibilityColumn { id: string; name: string }
export interface ColumnVisibility {
  columns: VisibilityColumn[]; hidden: string[]; toggle: (id: string) => void; showAll: () => void
}
const changed = 'blerg-board-columns-changed'
const moved = 'blerg-board-card-moved'
const storageKey = (person: string, board: string) => `blerg-board:columns:${JSON.stringify([person, board])}`
function read(person: string, board: string): string[] {
  if (!person) return []
  try {
    const value: unknown = JSON.parse(localStorage.getItem(storageKey(person, board)) ?? '[]')
    return Array.isArray(value) ? value.filter((id): id is string => typeof id === 'string') : []
  } catch { return [] }
}

// Unknown identities may customize the current view, but never read or save another person's choice.
export function useColumnVisibility(person: string, board: string, columns: VisibilityColumn[], scope?: string): ColumnVisibility {
  const key = scope ? `blerg-board:tab-columns:${scope}` : storageKey(person, board)
  // A scoped (per-tab) view starts fresh: it never reads the shared board-wide default, or it
  // would leak that default — and so look shared — into every tab that has not chosen yet.
  const readView = () => {
    if (scope) { try { const saved: unknown = JSON.parse(localStorage.getItem(key) ?? 'null'); return Array.isArray(saved) ? saved.filter((id): id is string => typeof id === 'string') : [] } catch { return [] } }
    return read(person, board)
  }
  const [state, setState] = useState(() => ({ key, hidden: readView() }))
  if (state.key !== key) setState({ key, hidden: readView() })
  useEffect(() => {
    const update = (event: Event) => {
      if (event instanceof StorageEvent && event.key !== key && event.key !== null) return
      if (event instanceof CustomEvent && event.detail !== key) return
      setState({ key, hidden: readView() })
    }
    if (scope) return
    window.addEventListener('storage', update); window.addEventListener(changed, update)
    return () => { window.removeEventListener('storage', update); window.removeEventListener(changed, update) }
  }, [key, person, board, scope])
  useEffect(() => {
    if (!scope || !person || state.key !== key) return
    try { localStorage.setItem(key, JSON.stringify(state.hidden)) } catch { /* Optional storage. */ }
  }, [scope, person, key, state])
  const hidden = state.key === key ? state.hidden : readView()
  const save = (ids: string[]) => {
    setState({ key, hidden: ids })
    if (!person) return
    try {
      localStorage.setItem(key, JSON.stringify(ids))
      window.dispatchEvent(new CustomEvent(changed, { detail: key }))
    } catch { /* This view still works when storage is unavailable. */ }
  }
  return { columns, hidden, toggle: id => save(hidden.includes(id) ? hidden.filter(c => c !== id) : [...hidden, id]), showAll: () => save([]) }
}

export function announceBoardMove(board: string, column: string, reference: string) {
  window.dispatchEvent(new CustomEvent(moved, { detail: { board, column, reference } }))
}

export function HiddenColumnsNotice({ boardId, visibility, cards, counts }: {
  boardId: string; visibility: ColumnVisibility; cards: { column_id?: string | null }[]; counts?: Record<string, number>
}) {
  const [lastMove, setLastMove] = useState<{ board: string; column: string; reference: string } | null>(null)
  useEffect(() => {
    const receive = (e: Event) => setLastMove((e as CustomEvent).detail)
    window.addEventListener(moved, receive)
    return () => window.removeEventListener(moved, receive)
  }, [])
  const hidden = visibility.columns.filter(c => visibility.hidden.includes(c.id))
  if (!hidden.length) return null
  const destination = lastMove?.board === boardId && hidden.find(c => c.id === lastMove.column)
  return <div className="hidden-columns-notice" role="status">
    {destination && <span>{lastMove!.reference} moved to hidden column {destination.name}. </span>}
    <span>{hidden.length === visibility.columns.length ? 'No columns shown. ' : ''}Hidden: {hidden.map(c => `${c.name} (${counts?.[c.id] ?? cards.filter(card => card.column_id === c.id).length})`).join(', ')}. </span>
    <button className="active-clear" type="button" onClick={visibility.showAll}>Show all columns</button>
  </div>
}
