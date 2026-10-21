// A pinned ticket as the session panel shows it. Any tracker's pin is the agent's copy, in the chat's
// ticket dialog. A Blerg Board card pinned with its board (source "blerg-board", board_id, key
// "#<number>") is shown LIVE instead: the card as the board holds it now, read with the person's own
// board permissions (lib/liveBoardCard.ts), with its column, tags, where it was filed from, repos and
// trail, a Move that works in place, and the Board's page for the rest (Run, Discuss, Archive), inside
// Blerg. When the board cannot be read the agent's copy is shown, and says so.
import { useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { Markdown, TicketDetailChips, TicketDetails, TicketModal, TimeLabel, type Ticket } from '@blerglab/chat'
import { boardCardUrl, useBoardOrigin } from '../lib/boardLink'
import { boardCardRef, moveLiveBoardCard, useLiveBoardCard, LiveBoardCardError, type LiveBoardCard } from '../lib/liveBoardCard'
import './BoardCardView.css'

/** The mark of a chip that shows the live card. */
export function LiveMark() {
  return <span className="board-live-mark" title="Live from the board" aria-label="live from the board" role="img">●</span>
}

function liveTicket(pinned: Ticket, card: LiveBoardCard | null, url: string): Ticket {
  if (!card) return { ...pinned, ...(url ? { url } : {}) }
  return {
    source: 'blerg-board', key: `#${card.number}`, board_id: card.board_id, title: card.title || pinned.title,
    ...(card.archived_at ? { status: 'Archived' } : card.column_name ? { status: card.column_name } : {}),
    ...(card.type ? { type: card.type } : {}), ...(card.priority ? { priority: card.priority } : {}),
    ...(card.tags?.length ? { labels: card.tags } : {}), ...(url ? { url } : {}),
  }
}

function Section({ title, testId, children }: { title: string; testId?: string; children: ReactNode }) {
  return (
    <section className="ticket-section" data-testid={testId}>
      <h3 className="ticket-section-title">{title}</h3>
      {children}
    </section>
  )
}

function trailLine(e: { type: string; to_column?: string }): string {
  if (e.type === 'moved' || e.to_column) return `moved → ${e.to_column || 'another column'}`
  return e.type.replace(/_/g, ' ')
}

function MoveCard({ card, sessionId, onMoved }: { card: LiveBoardCard; sessionId: string; onMoved?: () => void }) {
  const columns = (card.columns ?? []).filter(c => c.id !== card.column_id)
  const [to, setTo] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  if (columns.length === 0 || card.archived_at) return null
  const move = async () => {
    if (!to || busy) return
    setBusy(true)
    setError(null)
    try {
      await moveLiveBoardCard(card.board_id, card.number, to, sessionId)
      setTo('')
      onMoved?.()
    } catch (e) {
      setError(e instanceof LiveBoardCardError ? e.message : 'The card was not moved.')
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="board-card-move">
      <label className="board-card-move-label">
        <span>Move to</span>
        <select value={to} onChange={e => setTo(e.target.value)} aria-label="Move to column" disabled={busy}>
          <option value="">Choose a column…</option>
          {columns.map(c => <option key={c.id} value={c.id}>{c.name}</option>)}
        </select>
      </label>
      <button type="button" onClick={() => void move()} disabled={!to || busy}>{busy ? 'Moving…' : 'Move'}</button>
      {error && <span className="board-card-error" role="alert">{error}</span>}
    </div>
  )
}

function LiveCardBody({ card, pinned, sessionId, boardHref, onMoved, onShow }: {
  card: LiveBoardCard; pinned: Ticket; sessionId: string; boardHref: string; onMoved?: () => void; onShow?: () => void
}) {
  const trail = [...(card.trail ?? [])].reverse()
  const origin = card.origin
  const asPinned = pinned.status && pinned.status !== (card.archived_at ? 'Archived' : card.column_name)
  return (
    <div className="ticket-modal-grid">
      <div className="ticket-main board-card-live" data-testid="board-card-live">
        <TicketDetailChips ticket={liveTicket(pinned, card, '')} />
        {asPinned && <p className="board-card-note">The agent pinned it as “{pinned.status}”; this is the card now.</p>}
        <Section title="Actions" testId="board-card-actions">
          <MoveCard card={card} sessionId={sessionId} onMoved={onMoved} />
          <p className="board-card-note">
            Run, discuss or archive it on the Board: <a href={boardHref} target="_blank" rel="noopener" data-testid="board-card-open">Open in Board</a>
          </p>
        </Section>
        {origin && (origin.repo || origin.session_url || origin.engine) && (
          <Section title="Filed from" testId="board-card-origin">
            <ul className="ticket-detail-chips">
              {origin.repo && <li className="ticket-detail-chip"><span className="ticket-detail-name">Repository</span> {origin.repo}</li>}
              {origin.commit && <li className="ticket-detail-chip"><span className="ticket-detail-name">Commit</span> <code>{origin.commit.slice(0, 12)}</code></li>}
              {origin.engine && <li className="ticket-detail-chip"><span className="ticket-detail-name">Engine</span> {origin.engine}</li>}
            </ul>
            <OriginSession url={origin.session_url} />
          </Section>
        )}
        {(card.repos?.length ?? 0) > 0 && (
          <Section title="Repos" testId="board-card-repos">
            <ul className="ticket-detail-chips">
              {card.repos!.map((r, i) => <li key={r} className="ticket-detail-chip">{r}{i === 0 && <span className="ticket-detail-name"> · primary</span>}</li>)}
            </ul>
          </Section>
        )}
        {card.body
          ? <Section title="Description"><Markdown text={card.body} className="md-doc ticket-description" /></Section>
          : <p className="ticket-empty">No description.</p>}
        {trail.length > 0 && (
          <Section title="Trail" testId="board-card-trail">
            <ol className="board-card-trail">
              {trail.map((e, i) => (
                <li key={i} className={e.type === 'comment' ? 'comment' : undefined}>
                  {e.type === 'comment' ? <Markdown text={e.text ?? ''} className="board-card-comment" /> : <span>{trailLine(e)}</span>}
                  <span className="board-card-who">{e.actor} · <TimeLabel ts={e.created_at} /></span>
                </li>
              ))}
            </ol>
          </Section>
        )}
        {onShow && <button type="button" className="ticket-more" onClick={onShow}>Show in conversation</button>}
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
export function PinnedTicketModal({ ticket, sessionId, onClose, onShow }: {
  ticket: Ticket; sessionId: string; onClose: () => void; onShow?: () => void
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
        {live.state === 'failed' && <p className="board-card-error" role="alert">Could not read the card again: {live.error} <button type="button" onClick={live.refresh}>Retry</button></p>}
        <LiveCardBody card={card} pinned={ticket} sessionId={sessionId} boardHref={boardHref} onShow={onShow} />
      </>
    )
  } else if (live.state === 'failed') {
    body = (
      <>
        <p className="board-card-error" role="alert" data-testid="board-card-failed">
          {live.status === 403 ? 'Your sign-in cannot read this card on the board.' : `Could not read the card from the board: ${live.error}`} This is the agent's copy.{' '}
          <button type="button" onClick={live.refresh}>Retry</button>
        </p>
        <TicketDetails ticket={ticket}>{onShow && <button type="button" className="ticket-more" onClick={onShow}>Show in conversation</button>}</TicketDetails>
      </>
    )
  } else {
    body = <p className="board-card-note" role="status">Reading the card from the board…</p>
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
