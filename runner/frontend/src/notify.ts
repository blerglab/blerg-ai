import type { SessionInfo } from './types'

// sessionLabel picks a human name for a session in notifications, preferring the
// title, then the repo, then the first segment of the id. Mirrors the server's
// sessionLabel so message toasts and OS pushes read identically.
export function sessionLabel(s: Pick<SessionInfo, 'title' | 'repo' | 'id'>): string {
  if (s.title) return s.title
  if (s.repo) return s.repo
  const dash = s.id.indexOf('-')
  return dash > 0 ? s.id.slice(0, dash) : s.id
}
