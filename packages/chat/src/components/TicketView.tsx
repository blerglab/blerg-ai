// A pinned ticket, drawn the same for every tracker (the source only names the badge): its key
// (linked when the address is http(s)), title, status/type/priority chips, people, labels, the
// description (collapsed after a few lines), extra fields, the latest comments (collapsed) and the
// acceptance criteria. TicketDetails is the body the chat's card and the panel's TicketModal share.
//
// Tracker data is untrusted, and any session token can claim any source: every field is text; the
// description and the comments go through Markdown WITHOUT the app's cards (a ```card fence stays
// a code block) and, as everywhere, without raw HTML or images.
import { useEffect, useId, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useBackdropClose } from '../hooks/useBackdropClose'
import { sourceLabel, ticketHref } from '../model/worklist'
import type { Ticket } from '../types'
import Markdown from './Markdown'
import './worklist.css'

/** How long a description may be before it starts collapsed. */
const LONG_LINES = 8
const LONG_CHARS = 600

function isLong(text: string): boolean {
  return text.length > LONG_CHARS || text.split('\n').length > LONG_LINES
}

/** The tracker badge: the source's name; who pinned it said in its tooltip. */
export function SourceBadge({ source }: { source: string }) {
  const label = sourceLabel(source) || 'ticket'
  return (
    <span className="ticket-source" title={`Pinned by the agent · source: ${source || 'unknown'}`} aria-label={`${label} ticket, pinned by the agent`}>
      {label}
    </span>
  )
}

/** The key, as a link to the tracker when the address is http(s). */
export function TicketKey({ ticket }: { ticket: Pick<Ticket, 'key' | 'url'> }) {
  const href = ticketHref(ticket.url)
  if (!href) return <span className="ticket-key">{ticket.key}</span>
  return (
    <a className="ticket-key" href={href} target="_blank" rel="noopener noreferrer" onClick={e => e.stopPropagation()}>
      {ticket.key}
    </a>
  )
}

export function TicketChips({ ticket }: { ticket: Pick<Ticket, 'status' | 'type' | 'priority'> }) {
  const chips = [
    ticket.status && { k: 'status', label: ticket.status },
    ticket.type && { k: 'type', label: ticket.type },
    ticket.priority && { k: 'priority', label: ticket.priority },
  ].filter((c): c is { k: string; label: string } => !!c)
  if (chips.length === 0) return null
  return (
    <span className="ticket-chips">
      {chips.map(c => <span key={c.k} className={`ticket-chip ${c.k}`} title={c.k}>{c.label}</span>)}
    </span>
  )
}

function Description({ text }: { text: string }) {
  const long = isLong(text)
  const [open, setOpen] = useState(!long)
  return (
    <div className="ticket-description" data-testid="ticket-description">
      <div className={`ticket-description-body${open ? '' : ' collapsed'}`}>
        <Markdown text={text} />
      </div>
      {long && (
        <button type="button" className="ticket-more" aria-expanded={open} onClick={() => setOpen(o => !o)}>
          {open ? 'Show less' : 'Show all'}
        </button>
      )}
    </div>
  )
}

/** TicketDetails: the ticket's body (everything under its header line). */
export function TicketDetails({ ticket }: { ticket: Ticket }) {
  const people = [
    ticket.assignee && `Assignee: ${ticket.assignee}`,
    ticket.reporter && `Reporter: ${ticket.reporter}`,
  ].filter(Boolean)
  const comments = ticket.comments ?? []
  return (
    <div className="ticket-details">
      {people.length > 0 && <div className="ticket-people">{people.join(' · ')}</div>}
      {ticket.labels && ticket.labels.length > 0 && (
        <div className="ticket-labels" aria-label="Labels">
          {ticket.labels.map((l, i) => <span key={i} className="ticket-label">{l}</span>)}
        </div>
      )}
      {ticket.description && <Description text={ticket.description} />}
      {ticket.fields && ticket.fields.length > 0 && (
        <dl className="ticket-fields">
          {ticket.fields.map((f, i) => (
            <div key={i}><dt>{f.name}</dt><dd>{f.value}</dd></div>
          ))}
        </dl>
      )}
      {ticket.acceptance && ticket.acceptance.length > 0 && (
        <div className="ticket-acceptance">
          <div className="ticket-subhead">Acceptance criteria</div>
          <ul>{ticket.acceptance.map((a, i) => <li key={i}>{a}</li>)}</ul>
        </div>
      )}
      {comments.length > 0 && (
        <details className="ticket-comments">
          <summary>{comments.length === 1 ? 'Latest comment' : `Latest ${comments.length} comments`}</summary>
          <ul>
            {comments.map((c, i) => (
              <li key={i} className="ticket-comment">
                {(c.author || c.at) && <div className="ticket-comment-head">{[c.author, c.at].filter(Boolean).join(' · ')}</div>}
                <Markdown text={c.body} />
              </li>
            ))}
          </ul>
        </details>
      )}
    </div>
  )
}

/** TicketHead: badge, key, chips, then the title. */
export function TicketHead({ ticket, titleId }: { ticket: Ticket; titleId?: string }) {
  return (
    <div className="ticket-head">
      <div className="ticket-line">
        <SourceBadge source={ticket.source} />
        <TicketKey ticket={ticket} />
        <TicketChips ticket={ticket} />
      </div>
      <div className="ticket-title" id={titleId}>{ticket.title}</div>
    </div>
  )
}

/** TicketModal: a ticket over the page (the session panel opens it). */
export function TicketModal({ ticket, onClose, onShow }: {
  ticket: Ticket
  onClose: () => void
  /** "Show in conversation", when the host can find its card. */
  onShow?: () => void
}) {
  const titleId = useId()
  const ref = useRef<HTMLDivElement | null>(null)
  const backdrop = useBackdropClose(onClose)
  useEffect(() => ref.current?.focus(), [])
  return createPortal(
    <div className="artifact-overlay" data-testid="ticket-modal-overlay" {...backdrop}>
      <div
        ref={ref}
        className="artifact-dialog ticket-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        data-testid="ticket-modal"
        onKeyDown={e => { if (e.key === 'Escape') { e.stopPropagation(); onClose() } }}
      >
        <header className="artifact-header">
          <div className="artifact-header-text"><TicketHead ticket={ticket} titleId={titleId} /></div>
          <button type="button" className="artifact-close" aria-label="Close" onClick={onClose}>✕</button>
        </header>
        <div className="artifact-body ticket-modal-body">
          <TicketDetails ticket={ticket} />
          {onShow && (
            <button type="button" className="ticket-more" onClick={onShow}>Show in conversation</button>
          )}
        </div>
      </div>
    </div>,
    document.body,
  )
}
