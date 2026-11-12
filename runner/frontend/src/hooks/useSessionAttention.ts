import type { SessionInfo } from '../types'
import { useQuestionItems, type AttentionItem } from './useQuestionItems'
import { useSessionStore } from './useSessionStore'
import { descendantsOf } from '../lib/sessionPresentation'
export { useDecisionAttention, useSessionDecisionCounts } from './useDecisionAttention'
export type { PendingPlan } from './useDecisionAttention'

export function useDescendants(rootId: string) {
  const value = useSessionStore(s => JSON.stringify(descendantsOf(rootId, s.sessions).map(({ id, title, status, unread, parent_session_id, started_by, cron_id, archived_at, paused_reason }) => ({ id, title, status, unread, parent_session_id, started_by, cron_id, archived_at, paused_reason }))))
  return JSON.parse(value) as ReturnType<typeof descendantsOf>
}
export function useAttentionItems(rootId: string, root?: SessionInfo) {
  const { items } = useQuestionItems(rootId, root)
  const grouped = new Map<string, { session: AttentionItem['session']; failed: boolean; count: number }>()
  for (const item of items) {
    const previous = grouped.get(item.session.id)
    if (previous) { previous.count++; previous.failed ||= item.kind === 'failed' }
    else grouped.set(item.session.id, { session: item.session, failed: item.kind === 'failed', count: 1 })
  }
  return [...grouped.values()]
}
