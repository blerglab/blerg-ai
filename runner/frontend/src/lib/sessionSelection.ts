import { create } from 'zustand'
import { useMemo } from 'react'
import { useSessionStore } from '../hooks/useSessionStore'
import type { SessionInfo, SessionStatus } from '../types'
import { getFocus, setFocus } from './sidebarPrefs'
import { nestChildren, searchTree } from './sessionTree'
import { isAutomated } from './startedBy'

export const ACTIVE_STATUSES = new Set<SessionStatus>(['starting', 'running', 'idle', 'waiting'])
export const useSessionListFilters = create<{
  query: string; focus: string | null; clusterConfigured: boolean
  setQuery(query: string): void; setPlace(focus: string | null): void; setCluster(configured: boolean): void
}>(set => ({ query: '', focus: getFocus(), clusterConfigured: false,
  setQuery: query => set({ query }), setPlace: focus => { setFocus(focus); set({ focus }) }, setCluster: clusterConfigured => set({ clusterConfigured }),
}))
const newest = (a: SessionInfo, b: SessionInfo) => Date.parse(b.started_at) - Date.parse(a.started_at) || a.id.localeCompare(b.id)

/** Both sidebar forms consume this selector; sections, search and place rules live here. */
export function selectSessionSections(sessions: SessionInfo[], query: string, focus: string | null, current?: string) {
  const q = query.trim().toLowerCase()
  const tree = searchTree(nestChildren(sessions), q)
  const ids = new Set(sessions.map(s => s.id))
  const automated = tree.top.filter(s => isAutomated(s, ids) && s.id !== current)
  const own = tree.top.filter(s => !isAutomated(s, ids))
  const starred = own.filter(s => ACTIVE_STATUSES.has(s.status) && s.starred).sort(newest)
  const inPlace = (s: SessionInfo) => !!q || !focus || (focus === 'cluster' ? s.runtime === 'cluster' : s.runtime !== 'cluster' && s.daemon_id === focus)
  const listed = own.filter(s => ACTIVE_STATUSES.has(s.status) && !s.starred && inPlace(s))
  const selected = sessions.find(s => s.id === current && !s.archived_at && !s.parent_session_id)
  if (selected && !starred.some(s => s.id === current) && !listed.some(s => s.id === current)) listed.push(selected)
  listed.sort(newest)
  const done = [...own.filter(s => !ACTIVE_STATUSES.has(s.status) && s.id !== current), ...sessions.filter(s => s.archived_at && (!q || `${s.title} ${s.repo}`.toLowerCase().includes(q)))]
  return { tree, automated, starred, listed, done, main: [...starred, ...listed] }
}

// List structure changes independently of card content. Keep the previous records for fields
// used by grouping/filtering/nesting; cards subscribe to their current record themselves.
// In particular, child output, task counts, unread flags and model updates cannot rebuild the list.
const STRUCTURE_FIELDS = ['id', 'parent_session_id', 'archived_at',
  'starred', 'started_at', 'ended_at', 'runtime', 'daemon_id', 'started_by', 'cron_id'] as const
function sessionListSelector(query: string) {
  let previous: SessionInfo[] = []
  let input: SessionInfo[] | undefined
  return (state: { sessions: SessionInfo[] }) => {
    if (state.sessions === input) return previous
    input = state.sessions
    // Orphans, children of archived parents and cycles are roots too (nestChildren's rule).
    const roots = new Set(nestChildren(state.sessions).top.map(session => session.id))
    const next = state.sessions.map((session, i) => {
      const prior = previous[i]
      return prior && STRUCTURE_FIELDS.every(key => Object.is(prior[key], session[key])) &&
        (!session.archived_at && !roots.has(session.id) || prior.status === session.status && prior.paused_reason === session.paused_reason) &&
        (!query || prior.title === session.title && prior.repo === session.repo) ? prior : session
    })
    if (next.length !== previous.length || next.some((session, i) => session !== previous[i])) previous = next
    return previous
  }
}
// Structure snapshots can contain stale child content; read live values with separate selectors.
export function useSessionListSessions() {
  const query = useSessionListFilters(s => s.query.trim().toLowerCase())
  const select = useMemo(() => sessionListSelector(query), [query])
  return useSessionStore(select)
}
