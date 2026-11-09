import { apiFetch } from '../apiFetch'
import { useSessionStore } from '../hooks/useSessionStore'

const origins = new Map<string, string>()
export function blackboardOrigin(sessionId: string): string {
 let origin = origins.get(sessionId)
 if (!origin) { origin = crypto.randomUUID(); origins.set(sessionId, origin) }
 return origin
}
export function blackboardAction(url: string, init: RequestInit): Promise<Response> {
 return apiFetch(url, { ...init, headers: { ...init.headers, 'X-Blackboard-Origin': blackboardOrigin(new URL(url, location.origin).searchParams.get('session') ?? '') } })
}
function workspaceRoot(id: string, parents: Map<string, string | null | undefined>): string {
 const seen = new Set<string>()
 while (!seen.has(id)) {
  seen.add(id)
  const parent = parents.get(id)
  if (!parent) break
  id = parent
 }
 return id
}
function workspaceParents(): Map<string, string | null | undefined> {
 return new Map(useSessionStore.getState().sessions.map(s => [s.id, s.parent_session_id]))
}
export function blackboardWorkspace(id: string): string {
 return workspaceRoot(id, workspaceParents())
}
export function blackboardWorkspaceSessions(id: string): string[] {
 const parents = workspaceParents()
 const root = workspaceRoot(id, parents)
 return [root, ...[...parents.keys()].filter(session => workspaceRoot(session, parents) === root)]
}
