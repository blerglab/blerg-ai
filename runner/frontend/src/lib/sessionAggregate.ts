import { useShallow } from 'zustand/react/shallow'
import type { SessionInfo } from '../types'
import { useSessionStore } from '../hooks/useSessionStore'
import { hasLiveParent } from './sessionTree'
import { displayLabel, displayState, pausedByRestart, PAUSED_WORDS, type DisplayState } from './displayState'

/** Sidebar cards and tiles describe the entire session tree, independent of list filters or
 *  collapsed children. Descendants contribute work; only a root’s own state asks the person.
 *  Child questions and pauses belong to their parent agent; background work keeps its quieter glyph. */
export function selectSessionAggregate(session: SessionInfo, sessions: readonly SessionInfo[]) {
  const children = new Map<string, SessionInfo[]>()
  for (const child of sessions) {
    if (!child.parent_session_id || child.archived_at) continue
    const siblings = children.get(child.parent_session_id) ?? []
    siblings.push(child)
    children.set(child.parent_session_id, siblings)
  }
  const tree: SessionInfo[] = []
  const seen = new Set<string>()
  const pending = [session]
  while (pending.length) {
    const current = pending.pop()!
    if (seen.has(current.id)) continue
    seen.add(current.id)
    tree.push(current)
    pending.push(...(children.get(current.id) ?? []))
  }
  const root = !hasLiveParent(session, sessions)
  const waiting = root && session.status === 'waiting' && !pausedByRestart(session)
  const paused = root && pausedByRestart(session)
  const running = tree.some(s => s.status === 'running')
  const starting = tree.some(s => s.status === 'starting')
  const tasks = tree.reduce((n, s) => n + (['idle', 'running', 'waiting', 'starting'].includes(s.status) ? s.background_tasks ?? 0 : 0), 0)
  const ownState = displayState(session)
  const state: DisplayState | 'waiting-parent' = waiting || paused ? 'waiting' : !root && ownState === 'waiting' ? 'waiting-parent' : running ? 'running' : starting ? 'starting' : tasks > 0 ? 'background' : ownState
  return { state, paused, tasks, label: paused ? PAUSED_WORDS : state === 'waiting-parent' ? 'Waiting for its parent session' : displayLabel(state, tasks) }
}

export function useSessionAggregate(session: SessionInfo) {
  return useSessionStore(useShallow(s => selectSessionAggregate(session, s.sessions)))
}
