// A live approval must remain reviewable even when its conversation row is not loaded.
// Read only the owner's canonical request; deciding still sends only a choice to the existing CAS.
import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { DelegationReview, type AgentEvent, type DelegationChoice, type DelegationDecidedPayload, type DelegationRequestPayload } from '@blerglab/chat'
import { apiFetch } from '../apiFetch'

type Review = { event: AgentEvent; decision?: DelegationDecidedPayload }
const choices = new Set(['once', 'session', 'always', 'deny'])
function record(value: unknown): value is Record<string, unknown> { return !!value && typeof value === 'object' && !Array.isArray(value) }
function reviewOf(body: unknown, sessionId: string, requestId: string): Review {
  if (!record(body) || body.request_id !== requestId || body.parent_session_id !== sessionId || !record(body.request)) throw new Error('Request unavailable.')
  const request = body.request
  if ((typeof body.status !== 'string' || !['pending', 'allowed', 'denied', 'expired'].includes(body.status)) || typeof body.expires_at !== 'string' || !Number.isFinite(Date.parse(body.expires_at)) || !Array.isArray(body.offered) || !body.offered.every(c => typeof c === 'string' && choices.has(c)) || typeof request.engine !== 'string' || (typeof request.runtime !== 'string' || !['docker', 'daemon', 'cluster'].includes(request.runtime)) || typeof request.prompt !== 'string' || typeof request.title !== 'string') throw new Error('Request unavailable.')
  const payload: DelegationRequestPayload = {
    request_id: requestId, engine: request.engine, runtime: request.runtime as 'docker' | 'daemon' | 'cluster',
    daemon_name: request.runtime === 'cluster' ? 'the cluster' : 'the requested desktop', title: request.title, prompt: request.prompt,
    offered: body.offered as DelegationChoice[], expires_at: body.expires_at,
  }
  for (const key of ['model', 'effort', 'repo'] as const) if (typeof request[key] === 'string') payload[key] = request[key]
  if (request.repos !== undefined) {
    if (!Array.isArray(request.repos) || request.repos.length === 0 || request.repos.length > 20 || !request.repos.every(member => typeof member === 'string' && member.length > 0)) throw new Error('Request unavailable.')
    payload.repo = request.repos.join(', ')
  }
  if (typeof request.no_repo === 'boolean') payload.no_repo = request.no_repo
  // The board access the child gets: shown on the card, so the person decides knowing it.
  if (Array.isArray(request.boards)) {
    if (!request.boards.every(b => record(b) && typeof b.board === 'string' && typeof b.level === 'string' && (b.name === undefined || typeof b.name === 'string'))) throw new Error('Request unavailable.')
    payload.boards = (request.boards as { board: string; name?: string; level: string }[]).map(b => ({ board: b.board, name: b.name, level: b.level }))
  }
  const budget = request.budget
  if (record(budget) && typeof budget.engine === 'string' && typeof budget.available === 'boolean' && Array.isArray(budget.windows) && budget.windows.every(window => record(window) && typeof window.id === 'string' && typeof window.used_percent === 'number' && Number.isFinite(window.used_percent) && (window.resets_at == null || typeof window.resets_at === 'string'))) {
    payload.budget = { engine: budget.engine, available: budget.available, windows: budget.windows as NonNullable<DelegationRequestPayload['budget']>['windows'] }
    if (typeof budget.local === 'boolean') payload.budget.local = budget.local
    if (typeof budget.note === 'string') payload.budget.note = budget.note
  }
  const decision = body.status === 'pending' ? undefined : decisionOf(body, requestId)
  // A presentation-only row: never appended to history or given a fabricated durable sequence.
  return { event: { type: 'agent_event', session_id: sessionId, client_event_id: `live-review:${requestId}`, kind: 'delegation_request', ts: '', payload }, decision }
}

function decisionOf(body: unknown, requestId: string): DelegationDecidedPayload {
  if (!record(body) || body.request_id !== requestId || (typeof body.status !== 'string' || !['allowed', 'denied', 'expired'].includes(body.status))) throw new Error('Request unavailable.')
  const decision: DelegationDecidedPayload = { request_id: requestId, status: body.status as DelegationDecidedPayload['status'] }
  if (typeof body.choice === 'string' && ['once', 'session', 'always'].includes(body.choice)) decision.choice = body.choice as DelegationDecidedPayload['choice']
  if (typeof body.child_session_id === 'string') decision.child_session_id = body.child_session_id
  if (typeof body.start_error === 'string') decision.start_error = body.start_error
  return decision
}

export default function DelegationLiveReview(props: { sessionId: string; requestId: string }) {
  const [open, setOpen] = useState(false)
  return <><button type="button" className="rail-link" aria-expanded={open} onClick={() => setOpen(value => !value)}>{open ? 'Hide request' : 'Review request'}</button>{open && <ReviewBody key={`${props.sessionId}:${props.requestId}`} {...props} />}</>
}

function ReviewBody({ sessionId, requestId }: { sessionId: string; requestId: string }) {
  const navigate = useNavigate()
  const [review, setReview] = useState<Review | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    let active = true
    const controller = new AbortController()
    void apiFetch(`/api/delegation-requests/${encodeURIComponent(requestId)}`, { signal: controller.signal }).then(async response => {
      if (!response.ok) throw new Error('Request unavailable.')
      const next = reviewOf(await response.json(), sessionId, requestId)
      if (active) setReview(next)
    }).catch(() => { if (active) setError('Could not load this request. Close and reopen it to retry.') })
    return () => { active = false; controller.abort() }
  }, [sessionId, requestId])
  async function decide(id: string, choice: DelegationChoice): Promise<DelegationDecidedPayload> {
    if (id !== requestId) throw new Error('Request unavailable.')
    const response = await apiFetch(`/api/delegation-requests/${encodeURIComponent(id)}/decide`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ choice }),
    })
    if (!response.ok) throw Object.assign(new Error(response.status === 409 ? 'Already decided. Close and reopen this request to see the decision.' : 'Could not send the decision.'), { status: response.status })
    return decisionOf(await response.json(), requestId)
  }
  if (error) return <p role="alert">{error}</p>
  if (!review) return <p role="status">Loading request…</p>
  return <DelegationReview event={review.event} decision={review.decision} decide={decide} onOpenSession={id => navigate(`/sessions/${encodeURIComponent(id)}`)} />
}
