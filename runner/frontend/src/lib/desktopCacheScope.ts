import type { SessionInfo } from '../types'

let sessionScopes = new Map<string, string>()
// Transcripts and transport replay buffers outlive sidebar rows. Once cached,
// their authority must survive row replacement/redaction until invalidation.
const cachedScopes = new Map<string, string>()

export function updateDesktopSessionScopes(sessions: SessionInfo[]) {
  // Cluster launcher/pod IDs describe runtime startup, not desktop authority.
  sessionScopes = new Map(sessions.filter(s => s.daemon_id && s.runtime !== 'cluster').map(s => [s.id, s.daemon_id]))
}

export function rememberDesktopCacheScope(sessionId: string) {
  const desktopId = sessionScopes.get(sessionId)
  if (desktopId && !cachedScopes.has(sessionId)) cachedScopes.set(sessionId, desktopId)
}

export function desktopCacheScopeLost(sessions: SessionInfo[], complete: boolean) {
  const next = new Map(sessions.map(s => [s.id, s.daemon_id]))
  return [...cachedScopes].some(([id, desktopId]) =>
    (complete || next.has(id)) && next.get(id) !== desktopId)
}

export function clearDesktopCacheScopes() {
  cachedScopes.clear()
}
