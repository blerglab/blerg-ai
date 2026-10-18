import { create } from 'zustand'
import { onMessage } from '../ws'
import type { ChildrenChanged } from '../types'

const useSignals = create<{ counts: Record<string, number> }>(() => ({ counts: {} }))
export function refreshChildren(sessionId: string): void {
  useSignals.setState(s => {
    const counts = { ...s.counts, [sessionId]: (s.counts[sessionId] ?? 0) + 1 }
    while (Object.keys(counts).length > 200) delete counts[Object.keys(counts)[0]]
    return { counts }
  })
}
onMessage<ChildrenChanged>('children_changed', msg => refreshChildren(msg.session_id))
export function useChildrenSignal(sessionId: string): number {
  return useSignals(s => s.counts[sessionId] ?? 0)
}
