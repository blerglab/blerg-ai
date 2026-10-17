// A pinned ticket, drawn the same for every tracker (the source only names the badge), the way an
// issue tracker shows an issue: a header (badge, key, status chip, title), then the body. The chat's
// card is a summary (TicketSummary: one meta line and the start of the description); the modal is
// the issue view (TicketModal: description, acceptance criteria and comments, with a Details list
// beside them on a wide screen and above them on a narrow one).
//
// Tracker data is untrusted, and any session token can claim any source: every field is text; the
// description and the comments go through Markdown WITHOUT the app's cards (a ```card fence stays
// a code block) and, as everywhere, without raw HTML or images.
import { useEffect, useId, useRef, type KeyboardEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { useBackdropClose } from '../hooks/useBackdropClose'
import { parseTs } from '../model/timeLabel'
import { priorityGlyph, sourceLabel, statusTone, ticketHref, ticketMeta } from '../model/worklist'
import type { Ticket, TicketComment } from '../types'
import Markdown from './Markdown'
import { TimeLabel } from './TimeLabel'
import './worklist.css'

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

/** The status as a chip tinted by what it means (statusTone): gray to do, blue being worked,
 *  amber blocked, green done, faded canceled. Every status chip of the app is this one. */
export function StatusChip({ status }: { status: string | undefined }) {
  if (!status) return null
  const tone = statusTone(status)
  return (
    <span className={`ticket-chip ticket-status tone-${tone}`} data-tone={tone} data-testid="ticket-status" title="Status">
      {status}
    </span>
  )
}

/** The status chip, then the type and the priority as quiet chips. */
export function TicketChips({ ticket }: { ticket: Pick<Ticket, 'status' | 'type' | 'priority'> }) {
  if (!ticket.status && !ticket.type && !ticket.priority) return null
  return (
    <span className="ticket-chips">
      <StatusChip status={ticket.status} />
      {ticket.type && <span className="ticket-chip type" title="Type">{ticket.type}</span>}
      {ticket.priority && <span className="ticket-chip priority" title="Priority">{ticket.priority}</span>}
    </span>
  )
}

/** The priority with its glyph (↑ → ↓) drawn before it, out of the text. */
function Priority({ value }: { value: string }) {
  const glyph = priorityGlyph(value)
  return <span className="ticket-priority" data-glyph={glyph || undefined}>{value}</span>
}

const titleClass = (t: Pick<Ticket, 'status'>, base: string) =>
  `${base}${statusTone(t.status) === 'canceled' ? ' canceled' : ''}`

/** TicketHead: the badge, the key and the status, then the title. */
export function TicketHead({ ticket, titleId }: { ticket: Ticket; titleId?: string }) {
  return (
    <div className="ticket-head">
      <div className="ticket-line">
        <SourceBadge source={ticket.source} />
        <TicketKey ticket={ticket} />
        <StatusChip status={ticket.status} />
      </div>
      <div className={titleClass(ticket, 'ticket-title')} id={titleId}>{ticket.title}</div>
    </div>
  )
}

/** TicketSummary: the card's body under its head: one meta line (type · priority · assignee) and
 *  the first lines of the description, faded. */
export function TicketSummary({ ticket }: { ticket: Ticket }) {
  const meta = ticketMeta(ticket)
  return (
    <>
      {meta.length > 0 && (
        <div className="ticket-meta" data-testid="ticket-meta">
          {[
            ticket.type && <span key="t">{ticket.type}</span>,
            ticket.priority && <Priority key="p" value={ticket.priority} />,
            ticket.assignee && <span key="a">{ticket.assignee}</span>,
          ].filter(Boolean).flatMap((el, i) => (i === 0 ? [el] : [<span key={`s${i}`} className="ticket-meta-sep" aria-hidden="true"> · </span>, el]))}
        </div>
      )}
      {ticket.description && (
        <div className="ticket-excerpt" data-testid="ticket-excerpt">
          <Markdown text={ticket.description} />
        </div>
      )}
    </>
  )
}

/** "Bo Lee" → "BL", "ann" → "A"; '?' without a name. */
function initials(name: string): string {
  const words = name.trim().split(/\s+/).filter(Boolean)
  if (words.length === 0) return '?'
  return words.slice(0, 2).map(w => [...w][0]).join('').toUpperCase()
}

function Comment({ c }: { c: TicketComment }) {
  const at = c.at && (parseTs(c.at) !== null ? <TimeLabel ts={c.at} className="ticket-comment-time" /> : <span className="ticket-comment-time">{c.at}</span>)
  return (
    <li className="ticket-comment" data-testid="ticket-comment">
      <span className="ticket-avatar" aria-hidden="true">{initials(c.author ?? '')}</span>
      <div className="ticket-comment-box">
        <div className="ticket-comment-head">
          <span className="ticket-comment-author">{c.author || 'Unknown'}</span>
          {at}
        </div>
        <Markdown text={c.body} className="ticket-comment-body" />
      </div>
    </li>
  )
}

function Section({ title, testId, className, children }: { title: string; testId?: string; className?: string; children: ReactNode }) {
  return (
    <section className={`ticket-section${className ? ` ${className}` : ''}`} data-testid={testId}>
      <h3 className="ticket-section-title">{title}</h3>
      {children}
    </section>
  )
}

/** TicketDetails: the issue view's body: the Details list (first in the page; on a wide screen it
 *  sits beside the rest), then Description, Acceptance criteria and Comments. */
export function TicketDetails({ ticket, children }: { ticket: Ticket; children?: ReactNode }) {
  const rows: [string, ReactNode][] = []
  if (ticket.type) rows.push(['Type', ticket.type])
  if (ticket.priority) rows.push(['Priority', <Priority key="p" value={ticket.priority} />])
  if (ticket.assignee) rows.push(['Assignee', ticket.assignee])
  if (ticket.reporter) rows.push(['Reporter', ticket.reporter])
  if (ticket.labels?.length) {
    rows.push(['Labels', <span key="l" className="ticket-labels">{ticket.labels.map((l, i) => <span key={i} className="ticket-label">{l}</span>)}</span>])
  }
  for (const f of ticket.fields ?? []) rows.push([f.name, f.value])
  const comments = ticket.comments ?? []
  return (
    <div className="ticket-modal-grid">
      {rows.length > 0 && (
        <aside className="ticket-side">
          <Section title="Details">
            <dl className="ticket-fields" data-testid="ticket-modal-details">
              {rows.map(([name, value], i) => <div key={i}><dt>{name}</dt><dd>{value}</dd></div>)}
            </dl>
          </Section>
        </aside>
      )}
      <div className="ticket-main">
        {ticket.description
          ? (
              <Section title="Description" testId="ticket-modal-description">
                <Markdown text={ticket.description} className="md-doc ticket-description" />
              </Section>
            )
          : <p className="ticket-empty">No description.</p>}
        {ticket.acceptance && ticket.acceptance.length > 0 && (
          <Section title="Acceptance criteria" testId="ticket-modal-acceptance">
            <ul className="ticket-acceptance">
              {ticket.acceptance.map((a, i) => (
                <li key={i}><span className="ticket-box" aria-hidden="true" /><span>{a}</span></li>
              ))}
            </ul>
          </Section>
        )}
        {comments.length > 0 && (
          <Section title="Comments">
            <ul className="ticket-comments">{comments.map((c, i) => <Comment key={i} c={c} />)}</ul>
          </Section>
        )}
        {children}
      </div>
    </div>
  )
}

const FOCUSABLE = 'a[href], button:not([disabled]), input, select, textarea, summary, [tabindex]:not([tabindex="-1"])'

/** TicketModal: a ticket over the page, as an issue view. Esc, the backdrop and × close it; Tab
 *  stays inside it; the focus goes back where it was. */
export function TicketModal({ ticket, onClose, onShow }: {
  ticket: Ticket
  onClose: () => void
  /** "Show in conversation", when the host can find its card. */
  onShow?: () => void
}) {
  const titleId = useId()
  const ref = useRef<HTMLDivElement | null>(null)
  const backdrop = useBackdropClose(onClose)
  useEffect(() => {
    const before = document.activeElement as HTMLElement | null
    ref.current?.focus()
    return () => { if (before && before.isConnected) before.focus() }
  }, [])
  const href = ticketHref(ticket.url)
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'Escape') { e.stopPropagation(); onClose(); return }
    if (e.key !== 'Tab' || !ref.current) return
    const all = [...ref.current.querySelectorAll<HTMLElement>(FOCUSABLE)]
    if (all.length === 0) return
    const first = all[0]
    const last = all[all.length - 1]
    if (e.shiftKey && (document.activeElement === first || document.activeElement === ref.current)) { e.preventDefault(); last.focus() }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus() }
  }
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
        onKeyDown={onKeyDown}
      >
        <header className="artifact-header ticket-modal-header">
          <div className="artifact-header-text">
            <div className="ticket-line">
              <SourceBadge source={ticket.source} />
              <TicketKey ticket={ticket} />
              <StatusChip status={ticket.status} />
            </div>
            <h2 className={titleClass(ticket, 'ticket-modal-title')} id={titleId}>{ticket.title}</h2>
          </div>
          <div className="ticket-modal-actions">
            {href && (
              <a className="ticket-open-link" href={href} target="_blank" rel="noopener noreferrer">
                Open in {sourceLabel(ticket.source) || 'tracker'} <span aria-hidden="true">↗</span>
              </a>
            )}
            <button type="button" className="artifact-close" aria-label="Close" onClick={onClose}>✕</button>
          </div>
        </header>
        <div className="artifact-body ticket-modal-body">
          <TicketDetails ticket={ticket}>
            {onShow && (
              <button type="button" className="ticket-more" onClick={onShow}>Show in conversation</button>
            )}
          </TicketDetails>
        </div>
      </div>
    </div>,
    document.body,
  )
}
