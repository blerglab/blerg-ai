import type { SessionInfo } from '../types'
import { sessionRole } from './sessionRole'
import { hasLiveParent, rootSessionId } from './sessionTree'

export function sessionNoun(session: Pick<SessionInfo, 'parent_session_id' | 'started_by' | 'cron_id'>): 'workspace' | 'channel' | 'worker' {
  return session.parent_session_id || session.started_by?.kind === 'session' ? sessionRole(session) === 'worker' ? 'worker' : 'channel' : 'workspace'
}
export function suppressWorkerUnread(session: SessionInfo, sessions: readonly SessionInfo[]): boolean {
  return sessionRole(session) === 'worker' && hasLiveParent(session, sessions) && rootSessionId(sessions, session.id) !== session.id
}
export function descendantsOf(id: string, sessions: readonly SessionInfo[]): SessionInfo[] {
  const seen = new Set([id]); const result: SessionInfo[] = []
  for (let size = -1; size !== seen.size;) {
    size = seen.size
    for (const s of sessions) if (!s.archived_at && s.parent_session_id && seen.has(s.parent_session_id) && !seen.has(s.id)) { seen.add(s.id); result.push(s) }
  }
  return result
}
export function descendantSummary(children: readonly SessionInfo[]): string {
  const channels = children.filter(s => sessionRole(s) === 'channel').length
  const workers = children.filter(s => sessionRole(s) === 'worker').length
  const runs = children.filter(s => sessionRole(s) === 'run').length
  return [[channels, 'channel'], [workers, 'worker'], [runs, 'run']].filter(([n]) => Number(n) > 0).map(([n, noun]) => `${n} ${noun}${n === 1 ? '' : 's'}`).join(', ')
}
export function hasUnreadChannels(id: string, sessions: readonly SessionInfo[]): boolean {
  return descendantsOf(id, sessions).some(s => sessionRole(s) === 'channel' && s.unread)
}
/** How many things wait on the person across the items (each question and decision counts). */
export function attentionCount(items: readonly { count: number }[]) { return items.reduce((sum, item) => sum + item.count, 0) }
