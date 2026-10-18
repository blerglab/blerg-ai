// The session's request to start another session, and the person's answer. Pending, it offers
// exactly the choices the server offered (when the transport can decide); decided or expired, it
// says so in one line. Typing in the composer never decides: there is no reply box here.
import { useEffect, useRef, useState } from 'react'
import type { AgentEvent, DelegationChoice, DelegationDecidedPayload, DelegationRequestPayload } from '../../types'
import { budgetLine, decidedLine, engineLabel, runtimeWords, type DelegationState } from '../../model/delegations'
import { parseTs } from '../../model/timeLabel'
import { useDelegations } from '../delegationContext'
import { TimeLabel } from '../TimeLabel'

const EXCERPT = 500

export default function DelegationCard({ ev }: { ev: AgentEvent }) {
  const ctx = useDelegations()
  const p = ev.payload as DelegationRequestPayload
  const [now, setNow] = useState(() => Date.now())
  const [busy, setBusy] = useState(false)
  const inFlight = useRef(false)
  const [conflict, setConflict] = useState(false)
  const [error, setError] = useState('')
  const [local, setLocal] = useState<DelegationState | null>(null)
  const [full, setFull] = useState<string | null>(null)

  const expiresAt = parseTs(p.expires_at)
  const server = ctx?.states.get(p.request_id) ?? null
  const state: DelegationState = server?.decided ? server : local ?? server ?? { decided: null, decidedAt: null }
  const expired = !state.decided && expiresAt !== null && now >= expiresAt
  const pending = !state.decided && !expired

  // A pending card closes itself at its expiry.
  useEffect(() => {
    if (!pending || expiresAt === null) return
    const wait = expiresAt - Date.now()
    if (wait <= 0 || wait > 2_147_000_000) return
    const t = setTimeout(() => setNow(Date.now()), wait + 50)
    return () => clearTimeout(t)
  }, [pending, expiresAt])

  const engine = engineLabel(p.engine)
  const decide = ctx?.decide
  const offered = new Set<DelegationChoice>(Array.isArray(p.offered) ? p.offered : [])
  const unsandboxed = p.runtime === 'daemon'

  async function choose(choice: DelegationChoice) {
    if (!decide || inFlight.current || conflict) return
    inFlight.current = true
    setBusy(true)
    setError('')
    try {
      const r: DelegationDecidedPayload = await decide(p.request_id, choice)
      setLocal({ decided: r, decidedAt: new Date().toISOString() })
    } catch (e) {
      // 409: someone decided first. The buttons stay off until the server's decided event arrives.
      if ((e as { status?: number } | null)?.status === 409) {
        setConflict(true)
        setError('Already decided')
      } else {
        setError(e instanceof Error ? e.message : 'Could not send the decision.')
      }
    } finally {
      inFlight.current = false
      setBusy(false)
    }
  }

  async function showAll() {
    if (!ctx?.fullPrompt) return
    try {
      setFull(await ctx.fullPrompt(p.request_id))
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Could not load the prompt.')
    }
  }

  const prompt = full ?? (typeof p.prompt === 'string' ? p.prompt : '')
  const excerpt = full === null && prompt.length > EXCERPT ? `${prompt.slice(0, EXCERPT)}…` : prompt
  const canShowAll = full === null && !!ctx?.fullPrompt && (p.prompt_truncated === true || prompt.length > EXCERPT)
  const budget = budgetLine(p.budget, now)
  const line = decidedLine(state, expired)
  const child = state.decided?.child_session_id
  const startError = state.decided?.start_error
  const status = pending ? 'pending' : state.decided?.status ?? 'expired'

  return (
    <div className={`agent-card delegation-card status-${status}`} data-testid="delegation-card" data-event-id={ev.client_event_id}>
      <div className="delegation-head">
        <span className="delegation-title">This session wants to start a <b>{engine}</b> session</span>
        <TimeLabel ts={ev.ts} className="card-time-end" />
      </div>
      <div className="delegation-body">
        <div className="delegation-facts">
          {[p.model, p.effort].filter(Boolean).join(' · ') && <span>{[p.model, p.effort].filter(Boolean).join(' · ')}</span>}
          <span>{p.repo ? p.repo : 'no repository'}</span>
          <span className={unsandboxed ? 'delegation-warn' : undefined}>{runtimeWords(p.runtime)}</span>
        </div>
        {p.title && <div className="delegation-name">{p.title}</div>}
        {prompt && <div className="delegation-prompt" data-testid="delegation-prompt">{excerpt}</div>}
        {canShowAll && <button type="button" className="delegation-link" onClick={() => void showAll()}>Show all</button>}
        {budget && <div className="delegation-budget">{budget}</div>}
      </div>
      {pending && decide && (
        <div className="delegation-actions">
          {offered.has('once') && <button type="button" className="delegation-btn" disabled={busy || conflict} onClick={() => void choose('once')}>Allow once</button>}
          {offered.has('session') && <button type="button" className="delegation-btn" disabled={busy || conflict} onClick={() => void choose('session')}>Allow for this session</button>}
          {offered.has('always') && (
            <button
              type="button"
              className={`delegation-btn${unsandboxed ? ' warn' : ''}`}
              disabled={busy || conflict}
              title={`Applies to ${engine} ${unsandboxed ? 'on' : 'through'} ${p.daemon_name}`}
              onClick={() => void choose('always')}
            >
              {unsandboxed ? `Always allow ${engine} on this desktop (unsandboxed)` : `Always allow ${engine} in the desktop sandbox`}
            </button>
          )}
          {offered.has('deny') && <button type="button" className="delegation-btn" disabled={busy || conflict} onClick={() => void choose('deny')}>Deny</button>}
        </div>
      )}
      {pending && !decide && (
        <div className="delegation-readonly">
          {ctx?.decideUrl
            ? <a href={ctx.decideUrl} target="_blank" rel="noreferrer">Decide in Blerg Runner</a>
            : 'Decide in Blerg Runner'}
        </div>
      )}
      {error && <div className="delegation-error" role="alert" data-testid="delegation-error">{error}</div>}
      {line && (
        <div className="delegation-decided" data-testid="delegation-decided">
          <span>{line}</span>
          {child && (ctx?.onOpenSession
            ? <button type="button" className="delegation-link" onClick={() => ctx.onOpenSession?.(child)}>Open the session</button>
            : <span className="delegation-child">{child.slice(0, 8)}</span>)}
          {startError && <span className="delegation-start-error">{startError}</span>}
        </div>
      )}
    </div>
  )
}
