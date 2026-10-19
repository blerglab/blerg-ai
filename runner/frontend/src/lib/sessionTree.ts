import type { SessionInfo } from '../types'

export interface SessionTree { top: SessionInfo[]; childrenOf: ReadonlyMap<string, SessionInfo[]> }

export function nestChildren(sessions: readonly SessionInfo[]): SessionTree {
  const visible = sessions.filter(session => !session.parent_session_id || !session.archived_at)
  const listed = new Map(visible.map(s => [s.id, s]))
  const childrenOf = new Map<string, SessionInfo[]>()
  const top: SessionInfo[] = []
  for (const s of visible) {
    const seen = new Set([s.id])
    let parent = s.parent_session_id
    let cyclic = false
    while (parent && listed.has(parent)) {
      if (seen.has(parent)) { cyclic = true; break }
      seen.add(parent)
      parent = listed.get(parent)?.parent_session_id
    }
    if (!cyclic && s.parent_session_id && listed.has(s.parent_session_id)) {
      const children = childrenOf.get(s.parent_session_id) ?? []
      children.push(s)
      childrenOf.set(s.parent_session_id, children)
    } else top.push(s)
  }
  for (const children of childrenOf.values()) children.sort((a, b) => a.started_at.localeCompare(b.started_at) || a.id.localeCompare(b.id))
  return { top, childrenOf }
}

export function searchTree(tree: SessionTree, q: string): SessionTree {
  const query = q.trim().toLowerCase()
  if (!query) return tree
  const matches = (s: SessionInfo) => `${s.title} ${s.repo}`.toLowerCase().includes(query)
  const childrenOf = new Map<string, SessionInfo[]>()
  const keep = (s: SessionInfo): boolean => {
    const children = (tree.childrenOf.get(s.id) ?? []).filter(keep)
    if (children.length) childrenOf.set(s.id, children)
    return matches(s) || children.length > 0
  }
  return { top: tree.top.filter(keep), childrenOf }
}
