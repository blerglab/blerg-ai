import { useShallow } from 'zustand/react/shallow'
import { useSessionStore } from './useSessionStore'
import type { SessionInfo } from '../types'

export type BoardWorkerKinds = Record<string, NonNullable<SessionInfo['worker_kind']>>

// The board gets worker status/title/ticket from its live snapshot. Only the
// glyph kind comes from the session store; telemetry must not redraw every card.
export function useBoardWorkerKinds(children: readonly { id: string }[] = []): BoardWorkerKinds {
  return useSessionStore(useShallow(state => {
    const ids = new Set(children.map(child => child.id))
    const kinds: BoardWorkerKinds = {}
    for (const session of state.sessions) if (ids.has(session.id)) kinds[session.id] = session.worker_kind ?? 'card'
    return kinds
  }))
}
