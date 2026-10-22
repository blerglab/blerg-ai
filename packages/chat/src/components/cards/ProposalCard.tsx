// A proposal the session made, in the chat: a board card it wants to file, a new board it wants
// created (approved by the owner only, never for the session), or a call to an MCP tool the person
// set to approval mode. Pending, its owner approves, edits (board cards: in the host's
// editor), rejects, or allows it for the session (this one and later ones of exactly this scope).
// Decided, it says how; a created card follows as a compact ticket with Open. The event names the
// proposal only: what it says is read through the owner-only proposal API, so anyone else sees
// that it waits for the owner.
import { useCallback, useEffect, useRef, useState } from 'react'
import type { AgentEvent, FiledCard, ProposalDetail, ProposalRequestPayload, ProposalState } from '../../types'
import { outcomeLine } from '../../model/proposals'
import { useProposals } from '../proposalContext'
import { TicketChips } from '../TicketView'
import { TimeLabel } from '../TimeLabel'

type Access = 'loading' | 'ok' | 'denied' | 'error'

export default function ProposalCard({ ev }: { ev: AgentEvent }) {
  const ctx = useProposals()
  const raw = ev.payload as Partial<ProposalRequestPayload> | null
  const id = typeof raw?.proposal_id === 'string' ? raw.proposal_id : ''
  const kind = raw?.kind === 'board_file' || raw?.kind === 'board_create' ? raw.kind : 'mcp'
  // Why a session permission that covers it did not approve it (its hourly limit).
  const note = typeof raw?.note === 'string' ? raw.note.slice(0, 400) : ''
  const decided = id ? ctx?.states.get(id) : undefined
  const [detail, setDetail] = useState<ProposalDetail | null>(null)
  const [access, setAccess] = useState<Access>('loading')
  const [busy, setBusy] = useState(false)
  const inFlight = useRef(false)
  const [error, setError] = useState('')
  const [kept, setKept] = useState(false)
  const load = ctx?.load

  const reload = useCallback(async () => {
    if (!load || !id) { setAccess('denied'); return }
    try {
      setDetail(await load(id))
      setAccess('ok')
    } catch (e) {
      setAccess((e as { status?: number } | null)?.status === 404 ? 'denied' : 'error')
    }
  }, [load, id])
  // Read again whenever the transcript or the host says it changed.
  const decidedState = decided?.state
  const revision = ctx?.revision ?? 0
  useEffect(() => { void Promise.resolve().then(reload) }, [reload, decidedState, revision])

  if (!id) return null
  const state: ProposalState = detail && detail.state !== 'pending' ? detail.state : decided?.state ?? detail?.state ?? 'pending'
  const pending = state === 'pending'
  const card: FiledCard | null = decided?.card ?? detail?.card ?? null
  const auto = decided?.auto === true || !!detail?.approvedByGrant
  const decide = ctx?.decide
  const tool = detail?.tool || ''

  async function act(action: 'approve' | 'reject', allowForSession = false) {
    if (!decide || !detail || inFlight.current) return
    inFlight.current = true
    setBusy(true)
    setError('')
    setKept(false)
    try {
      setDetail(await decide(id, action, {
        ...(kind === 'board_file' || kind === 'board_create' ? { expected: detail.argumentsRaw } : {}),
        ...(allowForSession ? { allowForSession: true } : {}),
      }))
    } catch (e) {
      const status = (e as { status?: number } | null)?.status
      setKept((e as { permissionKept?: boolean } | null)?.permissionKept === true)
      setError(status === 409 ? `${e instanceof Error ? e.message : 'It changed.'} Review it again.` : e instanceof Error ? e.message : 'Could not send the decision.')
      if (status === 409) void reload()
    } finally {
      inFlight.current = false
      setBusy(false)
    }
  }

  const createdBoard = decided?.board ?? detail?.board ?? null
  const heading = kind === 'board_file'
    ? <>This session wants to file a card{detail?.target ? <> on <b>{detail.target}</b></> : null}</>
    : kind === 'board_create'
      ? <>This session wants to create a board{detail?.target ? <> named <b>{detail.target}</b></> : null}</>
      : <>This session wants to run <b>{tool || 'an MCP tool'}</b>{detail?.target ? <> on {detail.target}</> : null}</>
  const line = outcomeLine(kind, state, auto, createdBoard)

  return (
    <div className={`agent-card delegation-card proposal-card status-${state}`} data-testid="proposal-card" data-event-id={ev.client_event_id} data-proposal-id={id}>
      <div className="delegation-head">
        <span className="delegation-title">{heading}</span>
        <TimeLabel ts={ev.ts} className="card-time-end" />
      </div>
      {detail && (detail.title || detail.summary) && (
        <div className="delegation-body">
          {detail.title && <div className="delegation-name" data-testid="proposal-title">{detail.title}</div>}
          {detail.summary && <div className="delegation-prompt proposal-summary">{detail.summary}</div>}
        </div>
      )}
      {pending && note && <div className="delegation-readonly proposal-note" data-testid="proposal-note">{note}</div>}
      {pending && access === 'denied' && <div className="delegation-readonly">Waiting for the session owner's decision.</div>}
      {pending && access === 'error' && (
        <div className="delegation-readonly">Could not load this proposal. <button type="button" className="delegation-link" onClick={() => void reload()}>Retry</button></div>
      )}
      {pending && decide && detail && access === 'ok' && (
        <div className="delegation-actions" data-testid="proposal-actions">
          <button type="button" className="delegation-btn primary" disabled={busy} onClick={() => void act('approve')}>Approve</button>
          {kind === 'board_file' && ctx?.onEdit && <button type="button" className="delegation-btn" disabled={busy} onClick={() => ctx.onEdit?.(id)}>Edit</button>}
          <button type="button" className="delegation-btn" disabled={busy} onClick={() => void act('reject')}>Reject</button>
          {kind !== 'board_create' && <button
            type="button"
            className="delegation-btn"
            disabled={busy}
            title={kind === 'board_file' ? `Approve this, and file later cards on ${detail.target || 'this board'} without asking, in this session only` : `Approve this, and run ${tool || 'this tool'} without asking, in this session only`}
            onClick={() => void act('approve', true)}
          >
            Allow for this session
          </button>}
        </div>
      )}
      {error && (
        <div className="delegation-error" role="alert" data-testid="proposal-error">
          {error}
          {kept && ctx?.onManagePermissions && <> <button type="button" className="delegation-link" onClick={ctx.onManagePermissions}>Revoke in Permissions</button></>}
        </div>
      )}
      {line && (
        <div className="delegation-decided" data-testid="proposal-outcome">
          <span>{line}</span>
          {auto && ctx?.onManagePermissions && <button type="button" className="delegation-link" onClick={ctx.onManagePermissions}>Permissions</button>}
          {(state === 'failed' || state === 'unknown') && detail?.error && <span className="delegation-start-error">{detail.error}</span>}
        </div>
      )}
      {card && state === 'done' && <FiledTicket card={card} onOpen={ctx?.onOpenCard} />}
    </div>
  )
}

/** The created card, drawn like a pinned ticket's summary: board, number, title, the column as its
 *  status, type and priority, and Open (in the host's board, never a link out). */
export function FiledTicket({ card, onOpen }: { card: FiledCard; onOpen?: (card: FiledCard) => void }) {
  return (
    <div className="proposal-ticket" data-testid="proposal-ticket">
      <div className="ticket-line">
        <span className="ticket-source" title="A Blerg Board card">{card.board_name || 'Board'}</span>
        <span className="ticket-key">#{card.number}</span>
        <TicketChips ticket={{ status: card.column, type: card.type, priority: card.priority }} />
        {onOpen && <button type="button" className="delegation-btn proposal-open" onClick={() => onOpen(card)}>Open</button>}
      </div>
      {card.title && <div className="ticket-title">{card.title}</div>}
    </div>
  )
}
