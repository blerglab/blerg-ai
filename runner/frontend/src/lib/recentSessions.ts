// The sessions this person opened, most recent first, per account and per browser. The empty
// command palette offers them as Recent sessions; nothing else reads them.
import type { SessionInfo } from '../types'

const LIMIT = 50
const keyOf = (account: string) => `blerg-runner.recent-sessions.${encodeURIComponent(account)}`

export function recentSessionIds(account: string): string[] {
  if (!account) return []
  try {
    const saved = JSON.parse(localStorage.getItem(keyOf(account)) ?? '[]') as unknown
    return Array.isArray(saved) ? saved.filter((id): id is string => typeof id === 'string').slice(0, LIMIT) : []
  } catch { return [] }
}

export function rememberSessionVisit(account: string, sessionId: string): void {
  if (!account || !sessionId) return
  const next = [sessionId, ...recentSessionIds(account).filter(id => id !== sessionId)].slice(0, LIMIT)
  try { localStorage.setItem(keyOf(account), JSON.stringify(next)) } catch { /* Storage can be unavailable: recents are optional. */ }
}

const ENDED = new Set(['ended', 'stopped', 'error'])
/** Started by a cron (or another schedule), not by the person. */
export const isAutomatedSession = (s: SessionInfo) => !!s.cron_id || s.started_by?.kind === 'cron'
/** Worth offering before the person types: live, theirs to drive, not archived, not the one in view. */
export function offerSession(s: SessionInfo, current?: string): boolean {
  return s.id !== current && !s.archived_at && !s.ended_at && !ENDED.has(s.status) && !isAutomatedSession(s)
}
const OPEN = new Set(['starting', 'running', 'idle', 'waiting'])

/**
 * The empty palette's session order. Sessions the person opened come first, open ones before
 * ones whose machine is unreachable, then most recently opened first. Until the person has
 * opened any session that is still offered, it falls back to the offerable sessions they started
 * themselves, latest first. Sessions that are not offered are absent.
 */
export function recentSessionOrder(sessions: SessionInfo[], visited: string[], current?: string): string[] {
  const visitRank = new Map(visited.map((id, i) => [id, i]))
  const offered = sessions.filter(s => offerSession(s, current))
  const opened = offered.filter(s => visitRank.has(s.id))
  // The fallback is the person's own sessions: not ones a tool, the operator key or an agent started.
  const own = offered.filter(s => !s.started_by)
  const activity = (s: SessionInfo) => s.ended_at ?? s.started_at ?? ''
  return (opened.length ? opened : own).sort((a, b) =>
    Number(!OPEN.has(a.status)) - Number(!OPEN.has(b.status)) ||
    (visitRank.get(a.id) ?? Infinity) - (visitRank.get(b.id) ?? Infinity) ||
    activity(b).localeCompare(activity(a))
  ).map(s => s.id)
}
