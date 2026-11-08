import { useEffect, useRef, useState } from 'react'
import type { AgentEvent } from '../../types'
import type { ChannelRecommendation } from '../../transport/types'
import { useChat } from '../context'
import { TimeLabel } from '../TimeLabel'

export default function ChannelRecommendationCard({ ev }: { ev: AgentEvent }) {
  const ctx = useChat()
  const initial = (ev.payload ?? {}) as Partial<ChannelRecommendation>
  const [detail, setDetail] = useState<Partial<ChannelRecommendation>>(initial)
  const [ready, setReady] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const inFlight = useRef(false)
  const load = ctx?.channelRecommendation
  const session = ctx?.sessionId ?? ''
  useEffect(() => {
    let cancelled = false
    if (load && initial.id && initial.state === 'pending') void load(session, initial.id).then(value => { if (!cancelled) { setDetail(value); setReady(true) } }, () => { console.warn('recommendation_load_failed', { kind: 'channel', cause: 'transport_rejected' }); if (!cancelled) setError('Could not load the recommendation.') })
    return () => { cancelled = true }
  }, [load, session, initial.id, initial.state, ev.seq, ctx?.channelRecommendationRevision])
  async function decide(action: 'approve' | 'keep') {
    if (!ctx?.decideChannelRecommendation || !initial.id || inFlight.current) return
    inFlight.current = true; setBusy(true); setError('')
    try { setDetail(await ctx.decideChannelRecommendation(session, initial.id, action)) }
    catch (e) { console.warn('recommendation_decision_failed', { kind: 'channel', cause: 'transport_rejected' }); setError(e instanceof Error ? e.message : 'Could not save the decision.') }
    finally { inFlight.current = false; setBusy(false) }
  }
  if (!initial.id || typeof initial.topic !== 'string' || !['pending', 'kept', 'approved'].includes(initial.state ?? '')) return null
  return <div className={`agent-card delegation-card status-${detail.state}`} data-testid="channel-recommendation">
    <div className="delegation-head">
      <span className="delegation-title">Continue in a side channel?</span>
      <TimeLabel ts={ev.ts} className="card-time-end" />
    </div>
    {detail.state === 'pending' && <div className="delegation-prompt">{detail.topic}</div>}
    {detail.state === 'pending' && ready && ctx?.decideChannelRecommendation && <div className="delegation-actions">
      <button type="button" className="delegation-btn primary" disabled={busy} onClick={() => void decide('approve')}>Approve</button>
      <button type="button" className="delegation-btn" disabled={busy} onClick={() => void decide('keep')}>Keep in this thread</button>
    </div>}
    {detail.state !== 'pending' && <div className="delegation-decided">
      <span>{detail.state === 'kept' ? 'Kept in this thread' : 'Side channel approved'}</span>
      {detail.channel_id && <a className="delegation-link" href={`/sessions/${encodeURIComponent(detail.channel_id)}`}>Open channel</a>}
    </div>}
    {detail.state === 'pending' && !ctx?.decideChannelRecommendation && <div className="delegation-readonly">Waiting for the workspace owner.</div>}
    {error && <div className="delegation-error" role="alert">{error}</div>}
  </div>
}
