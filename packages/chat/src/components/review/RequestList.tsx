// RequestList: the margin list of a review's requests, newest first. Each shows where it points
// (the quote, the heading or page), what it asks, its status, and the session's reply when there is
// one. A request made in this sitting and not yet sent can be deleted. Hovering one focuses its
// highlight on the page.
import type { ReviewRequest } from '../../model/review'

interface Props {
  requests: ReviewRequest[]
  focusedId: string | null
  onHover: (id: string | null) => void
  /** Ids the person may still delete (made here, not yet submitted). */
  deletable: ReadonlySet<string>
  onDelete: (id: string) => void
}

const QUOTE_MAX = 80

const STATUS_LABEL: Record<ReviewRequest['status'], string> = { open: 'Open', done: 'Done', declined: 'Declined' }

function truncate(s: string): string {
  return s.length > QUOTE_MAX ? `${s.slice(0, QUOTE_MAX - 1)}…` : s
}

function where(r: ReviewRequest): string {
  const parts: string[] = []
  if (r.anchor.heading) parts.push(`Under “${r.anchor.heading}”`)
  if (r.anchor.page) parts.push(`page ${r.anchor.page}`)
  return parts.join(', ')
}

export default function RequestList({ requests, focusedId, onHover, deletable, onDelete }: Props) {
  const sorted = [...requests].sort((a, b) => (a.createdAt < b.createdAt ? 1 : a.createdAt > b.createdAt ? -1 : 0))
  if (sorted.length === 0) {
    return <p className="review-empty">No requests yet. Select text on the page and press N.</p>
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
            {r.status === 'open' && deletable.has(r.id) && (
              <button type="button" className="review-delete" aria-label={`Delete request: ${r.text}`} onClick={() => onDelete(r.id)}>✕</button>
            )}
          </div>
          {r.anchor.quote && <blockquote className="review-quote">{truncate(r.anchor.quote)}</blockquote>}
          {where(r) && <p className="review-where">{where(r)}</p>}
          <p className="review-text">{r.text}</p>
          {r.reply && <p className="review-reply">{r.reply}</p>}
        </li>
      ))}
    </ul>
  )
}
