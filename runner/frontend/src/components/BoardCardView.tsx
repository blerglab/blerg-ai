// A pinned ticket as the session panel shows it. Any tracker's pin is the agent's copy, in the chat's
// ticket dialog. A Blerg Board card pinned with its board (source "blerg-board", board_id, key
// "#<number>") is shown LIVE instead: the card as the board holds it now, read with the person's own
// board permissions (lib/liveBoardCard.ts), with its column, tags, where it was filed from, repos and
// trail, laid out like the Board's own card sheet: the description and the trail in a main column,
// the card's fields beside it (status as a column picker whose one click moves the card, through the
// session's board access; type, priority, tags, repos, the worker on it, where it was filed from),
// folded under the title on a narrow dialog. The header's "Open in Board" opens the Board's page for
// the rest (Run, Discuss, Archive). When the board cannot be read the agent's copy is shown, and says so.
import { useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { Markdown, TicketDetails, TicketModal, TimeLabel, type Ticket } from '@blerglab/chat'
import { boardCardUrl, useBoardOrigin } from '../lib/boardLink'
import { boardCardRef, moveLiveBoardCard, useLiveBoardCard, LiveBoardCardError, type LiveBoardCard } from '../lib/liveBoardCard'
import InitialLoader from './InitialLoader'
import './BoardCardView.css'

/** The mark of a chip that shows the live card. */
export function LiveMark() {
  return <span className="board-live-mark" title="Live from the board" aria-label="live from the board" role="img">●</span>
}

function liveTicket(pinned: Ticket, card: LiveBoardCard | null, url: string): Ticket {
  if (!card) return { ...pinned, ...(url ? { url } : {}) }
  return {
    source: 'blerg-board', key: card.reference || `#${card.number}`, board_id: card.board_id, title: card.title || pinned.title,
    ...(card.archived_at ? { status: 'Archived' } : card.column_name ? { status: card.column_name } : {}),
    ...(card.type ? { type: card.type } : {}), ...(card.priority ? { priority: card.priority } : {}),
    ...(card.tags?.length ? { labels: card.tags } : {}), ...(url ? { url } : {}),
  }
}

function trailLine(e: { type: string; to_column?: string }): string {
  if (e.type === 'moved' || e.to_column) return `moved → ${e.to_column || 'another column'}`
  return e.type.replace(/_/g, ' ')
}

/** One of the card's fields beside the description: a quiet label over its value, as the Board's
 *  sheet names them, never a section with a heading of its own. */
function Field({ label, testId, children }: { label: string; testId?: string; children: ReactNode }) {
  return (
    <div className="board-card-field" data-testid={testId}>
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  )
}

/** Status: the board's columns, the card's own pressed. One click on another moves the card there
 *  (through the session's board access); the card held is moved in place. */
function ColumnPicker({ card, sessionId }: { card: LiveBoardCard; sessionId: string }) {
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const columns = card.columns ?? []
  if (card.archived_at) return <span className="board-card-chip">Archived</span>
  if (columns.length === 0) return <span className="board-card-chip">{card.column_name || 'Unknown'}</span>
  const move = async (to: string) => {
    if (busy || to === card.column_id) return
    setBusy(to)
    setError(null)
    try {
      await moveLiveBoardCard(card.board_id, card.number, to, sessionId)
    } catch (e) {
      setError(e instanceof LiveBoardCardError ? e.message : 'The card was not moved.')
    } finally {
      setBusy(null)
    }
  }
  return (
    <>
      <div className="board-card-columns" role="group" aria-label="Status" data-testid="board-card-columns">
        {columns.map(c => {
          const on = c.id === card.column_id
          return (
            <button key={c.id} type="button" className="board-card-column" aria-pressed={on} aria-busy={busy === c.id || undefined}
              disabled={busy !== null} title={on ? `In ${c.name}` : `Move to ${c.name}`} onClick={() => void move(c.id)}>
              {c.name}
            </button>
          )
        })}
      </div>
      {busy && <span className="board-card-note" role="status">Moving…</span>}
      {error && <span className="board-card-error" role="alert">{error}</span>}
    </>
  )
}

function LiveCardBody({ card, pinned, sessionId, worker, onShow }: {
  card: LiveBoardCard; pinned: Ticket; sessionId: string; worker?: string; onShow?: () => void
}) {
  const trail = [...(card.trail ?? [])].reverse()
  const origin = card.origin
  const asPinned = pinned.status && pinned.status !== (card.archived_at ? 'Archived' : card.column_name)
  const tags = card.tags ?? []
  const repos = card.repos ?? []
  const assignee = worker ? null : pinned.assignee
  return (
    <div className="board-card-sheet-wrap">
      <div className="board-card-sheet" data-testid="board-card-live">
        <dl className="board-card-side" data-testid="board-card-side" aria-label="Card details">
          <Field label="Status"><ColumnPicker card={card} sessionId={sessionId} /></Field>
          {card.type && <Field label="Type"><span className="board-card-chip">{card.type}</span></Field>}
          {card.priority && <Field label="Priority"><span className="board-card-chip">{card.priority}</span></Field>}
          {tags.length > 0 && (
            <Field label="Tags" testId="board-card-tags">
              <ul className="board-card-chips">{tags.map(t => <li key={t} className="board-card-chip">{t}</li>)}</ul>
            </Field>
          )}
          {repos.length > 0 && (
            <Field label="Repos" testId="board-card-repos">
              <ul className="board-card-chips">
                {repos.map((r, i) => <li key={r} className="board-card-chip">{r}{i === 0 && <span className="board-card-chip-note"> · primary</span>}</li>)}
              </ul>
            </Field>
          )}
          {worker && (
            <Field label="Worker" testId="board-card-worker">
              <span className="board-card-worker" title={`${worker} works on it`}><span aria-hidden="true">↳</span> {worker}</span>
            </Field>
          )}
          {assignee && <Field label="Assignee"><span>{assignee}</span></Field>}
          {origin && (origin.repo || origin.session_url || origin.engine) && (
            <Field label="Filed from" testId="board-card-origin">
              <ul className="board-card-chips">
                {origin.repo && <li className="board-card-chip">{origin.repo}</li>}
                {origin.commit && <li className="board-card-chip"><code>{origin.commit.slice(0, 12)}</code></li>}
                {origin.engine && <li className="board-card-chip">{origin.engine}</li>}
              </ul>
              <OriginSession url={origin.session_url} />
            </Field>
          )}
        </dl>
        <div className="board-card-main" data-testid="board-card-main">
          {asPinned && <p className="board-card-note board-card-pinned-note">The agent pinned it as “{pinned.status}”; this is the card now.</p>}
          {card.body
            ? <div data-testid="board-card-description"><Markdown text={card.body} className="md-doc ticket-description board-card-description" /></div>
            : <p className="ticket-empty" data-testid="board-card-description">No description.</p>}
          {trail.length > 0 && (
            <section className="board-card-section" data-testid="board-card-trail">
              <h3 className="board-card-section-title">Trail</h3>
              <ol className="board-card-trail">
                {trail.map((e, i) => (
                  <li key={i} className={e.type === 'comment' ? 'comment' : undefined}>
                    {e.type === 'comment' ? <Markdown text={e.text ?? ''} className="board-card-comment" /> : <span>{trailLine(e)}</span>}
                    <span className="board-card-who">{e.actor} · <TimeLabel ts={e.created_at} /></span>
                  </li>
                ))}
              </ol>
            </section>
          )}
          {onShow && <button type="button" className="ticket-more" onClick={onShow}>Show in conversation</button>}
        </div>
      </div>
    </div>
  )
}

/** The session a card was filed from: a link inside this app when the address is this runner's own
 *  (the filer wrote it, so any other host is shown as text, never linked). */
function OriginSession({ url }: { url?: string }) {
  if (!url) return null
  let inApp: string | null = null
  try {
    const u = new URL(url)
    if (u.origin === location.origin && u.pathname.startsWith('/sessions/')) inApp = u.pathname + u.search
  } catch { /* not an address */ }
  return <p className="board-card-note" data-testid="board-card-origin-session">
    {inApp ? <Link to={inApp}>Originating session</Link> : <>Originating session: <span className="board-card-url">{url}</span></>}
  </p>
}

/** The ticket dialog of the session panel: a Blerg Board card live, any other pin as pinned. */
export function PinnedTicketModal({ ticket, sessionId, worker, onClose, onShow }: {
  ticket: Ticket; sessionId: string
  /** Who works on the card now (a subagent or a started session), by name: the side's ↳ Worker. */
  worker?: string
  onClose: () => void; onShow?: () => void
}) {
  const ref = boardCardRef(ticket)
  const live = useLiveBoardCard(ref, sessionId)
  const boardBase = useBoardOrigin()
  if (!ref) return <TicketModal ticket={ticket} onClose={onClose} onShow={onShow} />
  const card = live.read?.card ?? null
  const boardHref = live.read?.url || ticket.url || boardCardUrl(ref.board, ref.number, boardBase)
  let body: ReactNode
  if (card) {
    body = (
      <>
        {live.state === 'failed' && <p className="board-card-error" role="alert">Could not read the card again: {live.error} <button type="button" className="btn small" onClick={live.refresh}>Retry</button></p>}
        <LiveCardBody card={card} pinned={ticket} sessionId={sessionId} worker={worker} onShow={onShow} />
      </>
    )
  } else if (live.state === 'failed') {
    body = (
      <>
        <p className="board-card-error" role="alert" data-testid="board-card-failed">
          {live.status === 403 ? 'Your sign-in cannot read this card on the board.' : `Could not read the card from the board: ${live.error}`} This is the agent's copy.{' '}
          <button type="button" className="btn small" onClick={live.refresh}>Retry</button>
        </p>
        <TicketDetails ticket={ticket}>{onShow && <button type="button" className="ticket-more" onClick={onShow}>Show in conversation</button>}</TicketDetails>
      </>
    )
  } else {
    body = <InitialLoader loading loadKey={`${ref.board}:${ref.number}`} label="board card" shape="facts" />
  }
  return (
    <TicketModal
      ticket={liveTicket(ticket, card, boardHref)}
      onClose={onClose}
      body={body}
      actions={card ? <LiveMark /> : undefined}
    />
  )
}
