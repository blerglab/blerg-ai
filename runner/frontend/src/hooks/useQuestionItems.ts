import { useMemo, useSyncExternalStore } from 'react'
import type { MessageInfo, SessionInfo } from '../types'
import { useDecisionAttention, useSessionDecisionCounts, type PendingPlan } from './useDecisionAttention'
import { useMessageStore } from './useMessageStore'
import { useSessionStore } from './useSessionStore'
import { descendantsOf, suppressWorkerUnread } from '../lib/sessionPresentation'
import { rootSessionId } from '../lib/sessionTree'
import { useDismissStore } from '../lib/needsYouDismiss'
import type { LiveReply, LiveReviewRequest } from '../lib/liveState'

export type AttentionItem = { id: string; session: Pick<SessionInfo, 'id' | 'title' | 'runtime' | 'started_at'> } & (
  | { kind: 'ask'; question: MessageInfo }
  | { kind: 'review'; review: LiveReviewRequest }
  | { kind: 'plan'; plan: PendingPlan }
  | { kind: 'repository' | 'failed' | 'paused' | 'waiting' }
  | { kind: 'decision'; title: string; artifactId?: string }
)
/** One row per counted item. All surfaces use this collection, including unopened workers. */
export function collectAttentionItems(members: SessionInfo[], messages: MessageInfo[], live: Record<string, Partial<LiveReply>>, plans: PendingPlan[], hidden: ReadonlySet<string>): AttentionItem[] {
  const items: AttentionItem[] = []
  for (const session of members) {
    const start = items.length
    const add = (item: AttentionItem) => items.push(item)
    const reply = live[session.id]
    const closed = new Set(messages.filter(m => m.status !== 'open').map(m => m.id))
    const represented = new Set(messages.map(m => m.id))
    const ownAsks = [...(reply?.asks ?? []), ...Object.values(live).flatMap(r => (r.child_asks ?? []).filter(a => a.session_id === session.id).map(a => ({ ...a, event_id: a.message_id, seq: 0 })))]
    const seen = new Set<string>()
    const servedAsks: MessageInfo[] = ownAsks.filter(a => { const id = a.message_id || a.event_id; if (seen.has(id)) return false; seen.add(id); return true }).filter(a => !represented.has(a.message_id) && !closed.has(a.message_id)).map(a => ({ id: a.message_id || a.event_id, session_id: session.id, kind: 'ask', status: 'open', body: a.question, created_at: a.asked_at, no_wait: a.no_wait, owner_only: a.owner_only, answerable: !!a.message_id && reply?.can_release_prompt !== false }))
    for (const question of [...messages, ...servedAsks].filter(m => m.kind === 'ask' && m.status === 'open' && m.session_id === session.id && m.answerable === true && !hidden.has(`ask:${m.id}`))) add({ id: `ask:${question.id}`, session, kind: 'ask', question })
    for (const review of reply?.review_requests ?? []) if (!hidden.has(`review:${review.request_id}`)) add({ id: `review:${review.request_id}`, session, kind: 'review', review })
    if (session.repository_request && !session.repository_request.refusal) add({ id: `repository:${session.id}`, session, kind: 'repository' })
    for (const request of reply?.answer_requests ?? []) add({ id: `answer:${request.request_id}`, session, kind: 'decision', title: `Answer: ${request.artifact_name}`, artifactId: request.artifact_id })
    for (const request of reply?.delegation_requests ?? []) add({ id: `delegation:${request.request_id}`, session, kind: 'decision', title: `Approve worker: ${request.title || request.engine}` })
    for (let n = 0; n < (reply?.proposals ?? 0); n++) add({ id: `proposal:${session.id}:${n}`, session, kind: 'decision', title: 'Review proposal' })
    for (let n = 0; n < (reply?.tool_changes ?? 0); n++) add({ id: `tool:${session.id}:${n}`, session, kind: 'decision', title: 'Review tool change' })
    if (session.status === 'error') add({ id: `failed:${session.id}`, session, kind: 'failed' })
    else if (session.paused_reason === 'restart') add({ id: `paused:${session.id}`, session, kind: 'paused' })
    else if (!reply?.asks && items.length === start && session.status === 'waiting' && !suppressWorkerUnread(session, members)) add({ id: `waiting:${session.id}`, session, kind: 'waiting' })
  }
  const root = members[0]
  if (root) for (const plan of plans) items.push({ id: `plan:${plan.proposal.id}`, session: root, kind: 'plan', plan })
  return items
}
// Subscribe to the collection people see, rather than whole backing stores. Only fields that
// telemetry never changes (id, title, runtime, started_at) travel with each item. A worker's
// telemetry and another workspace's decisions must not wake every card and SessionDetail.
const subscribeAttention = (notify: () => void) => {
  const unsubscribe = [useSessionStore, useDecisionAttention, useMessageStore, useDismissStore].map(store => store.subscribe(notify))
  return () => unsubscribe.forEach(stop => stop())
}
export function useQuestionItems(sessionId: string, suppliedRoot?: SessionInfo) {
  const snapshot = useMemo(() => () => {
    const sessions = useSessionStore.getState().sessions
    const root = suppliedRoot ? sessionId : rootSessionId(sessions, sessionId) ?? sessionId
    const members = [suppliedRoot ?? sessions.find(s => s.id === root), ...descendantsOf(root, sessions)].filter((s): s is SessionInfo => !!s && !s.archived_at)
    const { live, plans } = useDecisionAttention.getState()
    const items = collectAttentionItems(members, useMessageStore.getState().messages, live, plans[root] ?? [], useDismissStore.getState().hidden)
      .map(item => ({ ...item, session: { id: item.session.id, title: item.session.title, runtime: item.session.runtime, started_at: item.session.started_at } }))
    return JSON.stringify({ root, items, ids: members.filter(s => sessions.some(known => known.id === s.id)).map(s => s.id) })
  }, [sessionId, suppliedRoot])
  const value = useSyncExternalStore(subscribeAttention, snapshot, snapshot)
  const result = useMemo(() => JSON.parse(value) as { root: string; items: AttentionItem[]; ids: string[] }, [value])
  useSessionDecisionCounts(result.ids)
  return result
}
export function questionItemCount(items: ReturnType<typeof useQuestionItems>) { return items.items.length }
