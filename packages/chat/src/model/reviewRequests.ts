// reviewRequests.ts: what a review request says. A session asks its owner to review one file or
// diff at an exact version (review_requested); the outcome comes back as review_decided. Pure
// functions.
import type { AgentEvent, ReviewRequestPayload, ReviewRequestState } from '../types'

const STATES: readonly ReviewRequestState[] = ['pending', 'approved', 'changes_requested', 'withdrawn', 'superseded', 'dismissed']
const text = (v: unknown, max: number) => (typeof v === 'string' ? v.slice(0, max) : '')
const count = (v: unknown) => (typeof v === 'number' && Number.isSafeInteger(v) && v > 0 ? v : 0)

/** A request from untrusted JSON (an event payload or the API's view), or null. */
export function readReviewRequest(v: unknown): ReviewRequestPayload | null {
  if (!v || typeof v !== 'object') return null
  const p = v as Record<string, unknown>
  if (typeof p.request_id !== 'string' || !p.request_id || typeof p.artifact_id !== 'string' || !p.artifact_id) return null
  if (!STATES.includes(p.state as ReviewRequestState)) return null
  const out: ReviewRequestPayload = {
    request_id: p.request_id,
    artifact_id: p.artifact_id,
    artifact_name: text(p.artifact_name, 300) || 'a file',
    artifact_version: count(p.artifact_version) || 1,
    artifact_view: text(p.artifact_view, 32),
    ask: text(p.ask, 1000),
    state: p.state as ReviewRequestState,
  }
  const note = text(p.note, 4000)
  if (note) out.note = note
  if (count(p.superseded_by)) out.superseded_by = count(p.superseded_by)
  if (typeof p.decided_at === 'string') out.decided_at = p.decided_at
  if (typeof p.created_at === 'string') out.created_at = p.created_at
  return out
}

/** Two readings of one request: a settled one wins over a pending one (decisions are final). */
export function newerReview(a: ReviewRequestPayload | undefined, b: ReviewRequestPayload): ReviewRequestPayload {
  if (!a) return b
  if (a.state === 'pending') return b
  return b.state === 'pending' ? a : b
}

/** By request_id: each request seen in the transcript, as it now stands. */
export function reviewRequestStates(events: readonly AgentEvent[]): ReadonlyMap<string, ReviewRequestPayload> {
  const out = new Map<string, ReviewRequestPayload>()
  for (const ev of events) {
    if (ev.kind !== 'review_requested' && ev.kind !== 'review_decided') continue
    const p = readReviewRequest(ev.payload)
    if (p) out.set(p.request_id, newerReview(out.get(p.request_id), p))
  }
  return out
}

/** The request that applies to one file version: its pending one, else the newest. */
export function reviewForArtifact(requests: Iterable<ReviewRequestPayload>, artifactId: string): ReviewRequestPayload | null {
  const rank = (r: ReviewRequestPayload) => (r.state === 'pending' ? 1 : 0)
  let best: ReviewRequestPayload | null = null
  for (const r of requests) {
    if (r.artifact_id !== artifactId) continue
    if (!best || rank(r) > rank(best) || (rank(r) === rank(best) && (r.created_at ?? '') >= (best.created_at ?? ''))) best = r
  }
  return best
}

/** One line for a settled request, or '' while it waits. */
export function reviewOutcomeLine(r: ReviewRequestPayload): string {
  switch (r.state) {
    case 'approved': return 'Approved'
    case 'changes_requested': return 'Changes requested'
    case 'withdrawn': return r.note ? `Withdrawn · ${r.note}` : 'Withdrawn by the session'
    case 'dismissed': return 'Dismissed (maybe answered in the chat)'
    case 'superseded': return r.superseded_by ? `Superseded by v${r.superseded_by}` : 'Superseded by a newer version'
    default: return ''
  }
}
