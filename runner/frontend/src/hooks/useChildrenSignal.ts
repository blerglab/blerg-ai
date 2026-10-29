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
// Notifications schedule a server read; they carry no renderable state of their own.
export function subscribeChildren(sessionId: string, notify: () => void): () => void {
  return useSignals.subscribe((state, previous) => {
    if (state.counts[sessionId] !== previous.counts[sessionId]) notify()
  })
}
