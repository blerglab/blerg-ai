import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useRefContext } from './RefLinks'
import './BoardSnapshot.css'

type Card = { number: number; title: string; tags?: string[]; priority?: string; assignee?: string }
type Column = { id: string; name: string; cards: Card[] }
type Snapshot = { version: 1; mode: 'board' | 'cards'; board_id: string; name: string; as_of: string; filters: Record<string, string | string[]>; columns: Column[] }
const text = (value: unknown): value is string => typeof value === 'string' && value.length <= 1000
/** Model-authored data: no URLs, HTML or filesystem paths are accepted as actions. */
export function parseBoardSnapshot(source: string): Snapshot | null {
  if (source.length > 2_000_000) return null
  try {
    const data = JSON.parse(source)
    if (data.version !== 1 || !['board', 'cards'].includes(data.mode) || !text(data.board_id) || !/^[A-Za-z0-9_-]{1,64}$/.test(data.board_id) || !text(data.name) || !text(data.as_of) || !Number.isFinite(Date.parse(data.as_of)) || !Array.isArray(data.columns) || data.columns.length > 100) return null
    if (!data.filters || typeof data.filters !== 'object' || Array.isArray(data.filters) || !Object.entries(data.filters).every(([key, value]) => text(key) && (text(value) || Array.isArray(value) && value.every(text)))) return null
    let count = 0
    const columns = new Set<string>()
    const numbers = new Set<number>()
    for (const col of data.columns) {
      if (!text(col.id) || columns.has(col.id) || !text(col.name) || !Array.isArray(col.cards)) return null
      columns.add(col.id)
      for (const card of col.cards) {
        if (++count > 5000 || !Number.isSafeInteger(card.number) || card.number < 1 || card.number > 9999999 || numbers.has(card.number) || !text(card.title) || card.tags !== undefined && (!Array.isArray(card.tags) || !card.tags.every(text)) || card.priority !== undefined && !text(card.priority) || card.assignee !== undefined && !text(card.assignee)) return null
        numbers.add(card.number)
      }
    }
    return data as Snapshot
  } catch { return null }
}

function SnapshotColumn({ column, board }: { column: Column; board: string }) {
  const [limit, setLimit] = useState(20)
  const [collapsed, setCollapsed] = useState(false)
  const ctx = useRefContext()
  const current = useRef(ctx)
  useLayoutEffect(() => { current.current = ctx }, [ctx])
  const mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  const [error, setError] = useState('')
  const [opening, setOpening] = useState<number | null>(null)
  async function open(card: Card) {
    if (!ctx?.refs?.boardCards || !ctx.refs.openCard || opening !== null) return
    setOpening(card.number); setError('')
    try {
      // Resolve under the current viewer's access; the snapshot cannot grant live access.
      const target = (await ctx.refs.boardCards(board, [card.number])).get(card.number)
      if (!mounted.current || current.current !== ctx) return
      if (target) ctx.refs.openCard(target)
      else setError('This card is unavailable or you do not have access.')
    } catch { setError('Could not open this card.') }
    finally { setOpening(null) }
  }
  return <section className="board-snapshot-column">
    <button className="artifact-btn" type="button" aria-expanded={!collapsed} onClick={() => setCollapsed(!collapsed)}>{column.name} ({column.cards.length})</button>
    {!collapsed && <div className="board-snapshot-tiles">{column.cards.slice(0, limit).map(card => <button className="board-snapshot-tile" key={card.number} type="button" disabled={!ctx?.refs?.boardCards || !ctx.refs.openCard || opening !== null} onClick={() => void open(card)}>
      <span className="board-snapshot-meta">#{card.number} · {card.priority || 'No priority'} {card.assignee ? <span title={card.assignee} aria-label={`Assignee: ${card.assignee}`}>◉ {card.assignee.slice(0, 1).toUpperCase()}</span> : <span title="Unassigned" aria-label="Unassigned">○</span>}</span>
      <strong>{card.title}</strong>
      {card.tags?.length ? <span className="board-snapshot-meta">{card.tags.join(' · ')}</span> : null}
    </button>)}</div>}
    {!collapsed && limit < column.cards.length && <button className="artifact-btn" type="button" onClick={() => setLimit(limit + 50)}>Show {Math.min(50, column.cards.length - limit)} more cards</button>}
    {error && <p role="status">{error}</p>}
  </section>
}

export default function BoardSnapshot({ data }: { data: Snapshot }) {
  const ctx = useRefContext()
  const boardUrl = ctx?.refs?.boardUrl?.(data.board_id)
  return <section className={`board-snapshot board-snapshot-${data.mode}`} aria-label="Board snapshot">
    <header><strong>{data.name}</strong> · {boardUrl ? <a href={boardUrl}>Open board</a> : <span>Board snapshot</span>}<div className="board-snapshot-meta">As of <time dateTime={data.as_of}>{data.as_of}</time></div><div className="board-snapshot-meta">Filters: {Object.entries(data.filters).map(([key, value]) => `${key}: ${Array.isArray(value) ? value.join(', ') : value}`).join('; ') || 'All cards'}</div></header>
    <div className="board-snapshot-columns">{data.columns.map(col => <SnapshotColumn key={col.id} column={col} board={data.board_id} />)}</div>
  </section>
}
