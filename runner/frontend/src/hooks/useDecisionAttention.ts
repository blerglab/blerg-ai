// Shared, bounded reads of pending decisions for chats whose transcripts are not open.
// The existing /live contract covers proposals, tool changes, reviews and answer pages.
import { useEffect } from 'react'
import { create } from 'zustand'
import { apiFetch } from '../apiFetch'
import type { BlackboardDraftChanged } from '../types'
import type { LiveReply, LiveReviewRequest } from '../lib/liveState'
import { invalidateBlackboardReads, readBlackboards } from '../lib/blackboardReads'
import type { PlanProposal } from '../components/BlackboardProposal'
import { onMessage } from '../ws'
import { useSessionStore } from './useSessionStore'

export type PendingPlan = { proposal: PlanProposal; milestoneName: string }
export const useDecisionAttention = create<{ counts: Record<string, number>; reviews: Record<string, LiveReviewRequest[]>; live: Record<string, Partial<LiveReply>>; plans: Record<string, PendingPlan[]> }>(() => ({ counts: {}, reviews: {}, live: {}, plans: {} }))
const refs = new Map<string, number>()
const queue = new Set<string>()
const readAt = new Map<string, number>()
const generations = new Map<string, number>()
const controllers = new Map<string, AbortController>()
let reading = false
let timer: ReturnType<typeof setTimeout> | undefined
const events = ['focus', 'blerg:proposals-changed', 'blerg:review-requests-changed', 'blerg:tool-changes-changed', 'blerg:repository-requests-changed']
function schedule() {
  if (refs.size && !timer) timer = setTimeout(() => { timer = undefined; refresh() }, 60_000)
}
function refresh(force = false) {
  if (document.hidden) { schedule(); return }
  for (const id of refs.keys()) if (force || Date.now() - (readAt.get(id) ?? 0) >= 60_000) queue.add(id)
  void drain()
}
async function drain() {
  if (reading) return
  reading = true
  try {
    while (queue.size) {
      const id = queue.values().next().value!
      queue.delete(id)
      if (!refs.has(id)) continue
      readAt.set(id, Date.now())
      const generation = generations.get(id)
      const controller = new AbortController()
      controllers.set(id, controller)
      const timeout = setTimeout(() => { console.warn('Attention read failed', { cause: 'timeout' }); controller.abort() }, 10_000)
      try {
        const aborted = new Promise<never>((_, reject) => controller.signal.addEventListener('abort', () => reject(new Error('Attention read cancelled')), { once: true }))
        const response = await Promise.race([apiFetch(`/api/sessions/${encodeURIComponent(id)}/live`, { signal: controller.signal }), aborted])
        if (!response?.ok) { console.warn('Attention read rejected', { status: response?.status }); continue }
        if (!refs.has(id) || generations.get(id) !== generation) continue
        const reply = await response.json() as Partial<LiveReply>
        if (reply.session_id !== id) { console.warn('Attention read failed', { cause: 'session_mismatch' }); continue }
        const count = (reply.repository_requests ?? 0) + (reply.proposals ?? 0) + (reply.tool_changes ?? 0) + (reply.review_requests?.length ?? 0) + (reply.answer_requests?.length ?? 0) + (reply.delegation_requests?.length ?? 0)
        if (refs.has(id) && generations.get(id) === generation) useDecisionAttention.setState(s => {
          const reviews = reply.review_requests ?? []
          const unchanged = JSON.stringify(s.reviews[id]) === JSON.stringify(reviews)
          const live = { ...s.live, [id]: reply }
          return s.counts[id] === count && unchanged && JSON.stringify(s.live[id]) === JSON.stringify(reply) ? s : { live, counts: { ...s.counts, [id]: count }, reviews: unchanged ? s.reviews : { ...s.reviews, [id]: reviews } }
        })
        const session = useSessionStore.getState().sessions.find(s => s.id === id)
        if (session && !session.parent_session_id) {
          const plans: PendingPlan[] = []
          for (const board of await Promise.race([readBlackboards(id), aborted])) {
            let cursor = ''
            const seen = new Set<string>()
            const cursors = new Set<string>()
            do {
              const response = await apiFetch(`/api/blackboards/${encodeURIComponent(board.id)}?session=${encodeURIComponent(id)}&limit=100${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`, { signal: controller.signal })
              if (!response.ok) { console.warn('Plan attention read rejected', { status: response.status }); throw new Error('Plan attention response failed') }
              const body = await response.json() as { record?: { proposals?: PlanProposal[] }; next_cursor?: string }
              for (const proposal of body.record?.proposals ?? []) if (proposal.status === 'pending' && !seen.has(proposal.id)) {
                seen.add(proposal.id)
                plans.push({ proposal, milestoneName: board.milestones?.find(m => m.id === proposal.milestone_id)?.name ?? board.name })
              }
              const next = body.next_cursor ?? ''
              if (next && cursors.has(next)) { console.warn('Plan attention read failed', { cause: 'repeated_cursor' }); throw new Error('Plan attention cursor repeated') }
              cursors.add(next)
              cursor = next
            } while (cursor)
          }
          if (refs.has(id) && generations.get(id) === generation) useDecisionAttention.setState(s => JSON.stringify(s.plans[id]) === JSON.stringify(plans) ? s : { plans: { ...s.plans, [id]: plans } })
        }
      } catch (cause) { if (!controller.signal.aborted) console.warn('Attention read failed', { cause: cause instanceof Error ? cause.name : 'unknown' }) } finally { clearTimeout(timeout); if (controllers.get(id) === controller) controllers.delete(id) }
    }
  } finally { reading = false; schedule() }
}
const changed = () => refresh(true)
onMessage<BlackboardDraftChanged>('blackboard_draft_changed', event => { invalidateBlackboardReads([event.session_id]); changed() })
export function useSessionDecisionCounts(ids: readonly string[]): Record<string, number> {
  const key = JSON.stringify([...new Set(ids)].sort())
  useEffect(() => {
    const watched: string[] = JSON.parse(key)
    for (const id of watched) { if (!refs.has(id)) generations.set(id, (generations.get(id) ?? 0) + 1); refs.set(id, (refs.get(id) ?? 0) + 1) }
    if (refs.size && !timer) {
      schedule()
      for (const event of events) window.addEventListener(event, changed)
      document.addEventListener('visibilitychange', changed)
    }
    refresh()
    return () => {
      for (const id of watched) { const n = (refs.get(id) ?? 1) - 1; if (n) refs.set(id, n); else { refs.delete(id); queue.delete(id); readAt.delete(id); controllers.get(id)?.abort(); useDecisionAttention.setState(s => { const counts = { ...s.counts }; const reviews = { ...s.reviews }; const live = { ...s.live }; const plans = { ...s.plans }; delete counts[id]; delete reviews[id]; delete live[id]; delete plans[id]; return { counts, reviews, live, plans } }) } }
      if (!refs.size) { clearTimeout(timer); timer = undefined; for (const event of events) window.removeEventListener(event, changed); document.removeEventListener('visibilitychange', changed) }
    }
  }, [key])
  const value = useDecisionAttention(s => JSON.stringify(Object.fromEntries((JSON.parse(key) as string[]).map(id => [id, s.counts[id] ?? 0]))))
  return JSON.parse(value)
}
