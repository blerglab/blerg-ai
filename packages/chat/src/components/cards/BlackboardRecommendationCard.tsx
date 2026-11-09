import { useEffect, useRef, useState } from 'react'
import type { AgentEvent } from '../../types'
import type { BlackboardRecommendation } from '../../transport/types'
import { useChat } from '../context'
import { TimeLabel } from '../TimeLabel'

export default function BlackboardRecommendationCard({ ev }: { ev: AgentEvent }) {
  const ctx = useChat()
  const initial = (ev.payload ?? {}) as Partial<BlackboardRecommendation>
  const [detail, setDetail] = useState<Partial<BlackboardRecommendation>>(initial)
  const [ready, setReady] = useState(false)
  const [busy, setBusy] = useState<'setup_with_main' | 'setup_myself' | 'not_now' | null>(null)
  const [error, setError] = useState('')
  const inFlight = useRef(false)
  const load = ctx?.blackboardRecommendation
  const session = ctx?.sessionId ?? ''
  useEffect(() => {
    let cancelled = false
    if (load && initial.id && initial.state === 'pending') void load(session, initial.id).then(value => { if (!cancelled) { setDetail(value); setReady(true) } }, () => { console.warn('recommendation_load_failed', { kind: 'blackboard', cause: 'transport_rejected' }); if (!cancelled) setError('Could not load the recommendation.') })
    return () => { cancelled = true }
  }, [load, session, initial.id, initial.state, ev.seq, ctx?.blackboardRecommendationRevision])
  async function decide(action: 'setup_with_main' | 'setup_myself' | 'not_now') {
    if (!ctx?.decideBlackboardRecommendation || !initial.id || inFlight.current) return
    inFlight.current = true; setBusy(action); setError('')
    try {
      setDetail(await ctx.decideBlackboardRecommendation(session, initial.id, action))
      if (action === 'setup_myself') ctx.openBlackboard?.()
    } catch (e) { console.warn('recommendation_decision_failed', { kind: 'blackboard', cause: 'transport_rejected' }); setError(e instanceof Error ? e.message : 'Could not save the decision.') }
    finally { inFlight.current = false; setBusy(null) }
  }
  if (!initial.id || typeof initial.reason !== 'string' || !['pending', 'setup_with_main', 'setup_myself', 'not_now'].includes(initial.state ?? '')) return null
  return <div className={`agent-card delegation-card status-${detail.state}`} data-testid="blackboard-recommendation">
    <div className="delegation-head">
      <span className="delegation-title">Set up a blackboard for this workspace?</span>
      <TimeLabel ts={ev.ts} className="card-time-end" />
    </div>
    {detail.state === 'pending' && <div className="delegation-prompt">{detail.reason}</div>}
    {detail.state === 'pending' && ready && ctx?.decideBlackboardRecommendation && <div className="delegation-actions">
      <button type="button" className="delegation-btn primary" disabled={!!busy} onClick={() => void decide('setup_with_main')}>Set it up with Main</button>
      {ctx.openBlackboard && <button type="button" className="delegation-btn" disabled={!!busy} onClick={() => ctx.openBlackboard?.()}>View blackboard</button>}
      <button type="button" className="delegation-btn" disabled={!!busy} onClick={() => void decide('not_now')}>Not now</button>
    </div>}
    {detail.state !== 'pending' && <div className="delegation-decided">
      {detail.state === 'setup_with_main' && <span>Sent to Main — it’ll follow up with you in the chat.</span>}
      {detail.state === 'setup_myself' && <span>Opened the blackboard.</span>}
      {detail.state === 'not_now' && <>
        <span>Put off for later.</span>
        {ctx?.openBlackboard && <button type="button" className="delegation-link" onClick={() => ctx.openBlackboard?.()}>Open blackboard</button>}
      </>}
    </div>}
    {detail.state === 'pending' && !ctx?.decideBlackboardRecommendation && <div className="delegation-readonly">Waiting for the workspace owner.</div>}
    {error && <div className="delegation-error" role="alert">{error}</div>}
  </div>
}
