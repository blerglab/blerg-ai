// RequestList: the margin list of a review's requests, newest first. Each shows where it points
// (the quote, the heading or page), what it asks, its status, and the session's reply when there is
// one. A request made in this sitting and not yet sent can be deleted. Hovering one focuses its
// highlight on the page.
import {useState} from 'react'
import Markdown from '../Markdown'
import ReopenThread from './ReopenThread'
import ThreadHistory from './ThreadHistory'
import { diffAnchorLabel, type ReviewRequest } from '../../model/review'

interface Props {
  onReopen?(id: string, explanation: string): void
  busy?: boolean
  pending?: ReadonlySet<string>
  emptyHint?: string
  onSelect?: (id: string) => void
  selectableArtifactId?: string
  requests: ReviewRequest[]
  focusedId: string | null
  onHover: (id: string | null) => void
  /** Ids the person may still delete (made here, not yet submitted). */
  deletable: ReadonlySet<string>
  onDelete: (id: string) => void
}

const QUOTE_MAX = 80

const STATUS_LABEL: Record<ReviewRequest['status'], string> = { open: 'Open', done: 'Resolved', declined: 'Rejected' }

function truncate(s: string): string {
  return s.length > QUOTE_MAX ? `${s.slice(0, QUOTE_MAX - 1)}…` : s
}

function where(r: ReviewRequest): string {
  const parts: string[] = []
  if (r.anchor.diff) parts.push(diffAnchorLabel(r.anchor.diff))
  if (r.anchor.heading) parts.push(`Under “${r.anchor.heading}”`)
  if (r.anchor.page) parts.push(`page ${r.anchor.page}`)
  return parts.join(', ')
}

export default function RequestList({ requests, focusedId, onHover, deletable, onDelete, emptyHint, onSelect, selectableArtifactId, onReopen, busy, pending }: Props) {
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(new Set())
  const sorted = [...requests].sort((a, b) => (a.createdAt < b.createdAt ? 1 : a.createdAt > b.createdAt ? -1 : 0))
  if (sorted.length === 0) {
    return <p className="review-empty">No requests yet. {emptyHint ?? 'Select text on the page and press Ctrl+Alt+M.'}</p>
  }
  return (
    <ul className="review-list">
      {sorted.map(r => (
        <li
          key={r.id}
          className={`review-request status-${r.status}${r.id === focusedId ? ' review-focus' : ''}`}
          data-testid="review-request"
          onMouseEnter={() => onHover(r.id)}
          onMouseLeave={() => onHover(null)}
        >
          <div className="review-request-head">
            <span className={`review-badge review-badge-${r.status}`}>{STATUS_LABEL[r.status]}</span>
            {pending?.has(r.id) && <span className="review-pending">Pending review</span>}
            {r.status !== 'open' && <button type="button" className="artifact-btn"
              aria-expanded={!collapsed.has(r.id)} aria-label={`${collapsed.has(r.id) ? 'Expand' : 'Collapse'} thread: ${r.text}`}
              onClick={() => setCollapsed(current => {const next = new Set(current);if (next.has(r.id)) next.delete(r.id);else next.add(r.id);return next})}>
              {collapsed.has(r.id) ? 'Expand thread' : 'Collapse thread'}
            </button>}
            {r.status === 'open' && r.revision === undefined && deletable.has(r.id) && (
              <button type="button" className="review-delete" aria-label={`Delete request: ${r.text}`} onClick={() => onDelete(r.id)}>✕</button>
            )}
          </div>
          {r.status !== 'open' && collapsed.has(r.id) ? <p className="review-thread-summary">{r.text}</p> : <>
          {r.anchor.quote && <blockquote className="review-quote">{truncate(r.anchor.quote)}</blockquote>}
          {where(r) && <p className="review-where">{where(r)}</p>}
          <div className="review-text"><Markdown text={r.text}/></div>
          {onSelect && r.anchor.diff?.artifactId === selectableArtifactId && (
            <button type="button" className="artifact-btn" aria-label={`Show lines for request: ${r.text}`} onClick={() => onSelect(r.id)}>Show lines</button>
          )}
          <ThreadHistory request={r}/>
          {r.status !== 'open' && onReopen && <ReopenThread request={r} busy={busy} onReopen={explanation => onReopen(r.id, explanation)}/>}
          </>}
        </li>
      ))}
    </ul>
  )
}
