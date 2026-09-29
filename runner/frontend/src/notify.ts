import type { SessionInfo, SessionStatus } from './types'

// sessionLabel picks a human name for a session in notifications, preferring the
// title, then the repo, then the first segment of the id. Mirrors the server's
// sessionLabel so toasts and OS pushes read identically.
export function sessionLabel(s: Pick<SessionInfo, 'title' | 'repo' | 'id'>): string {
  if (s.title) return s.title
  if (s.repo) return s.repo
  const dash = s.id.indexOf('-')
  return dash > 0 ? s.id.slice(0, dash) : s.id
}

// toastForTransition returns the toast content for a session entering `status`,
// or null for statuses that shouldn't notify. Only the "human needed" states —
// waiting and idle — produce a toast, matching the server's push policy.
export function toastForTransition(
  s: SessionInfo,
  status: SessionStatus,
): { title: string; body: string; url: string } | null {
  const label = sessionLabel(s)
  const url = `/sessions/${s.id}`
  if (status === 'waiting') {
    return { title: 'Session waiting', body: `${label} is waiting for your input.`, url }
  }
  if (status === 'idle') {
    return { title: 'Session idle', body: `${label} finished its turn.`, url }
  }
  return null
}
