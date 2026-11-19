import type { SessionInfo } from '../types'

/** Origin metadata distinguishes workers and runs; person chats are workspaces or channels.
 * An absent started_by is the contract for a chat started by the person. */
export function sessionRole(session: Pick<SessionInfo, 'parent_session_id' | 'started_by' | 'cron_id'>): 'workspace' | 'channel' | 'worker' | 'run' {
  if (session.cron_id || session.started_by?.kind === 'cron') return 'run'
  if (session.started_by?.kind === 'session' || session.started_by?.kind === 'agent' || session.started_by?.kind === 'runner_key') return 'worker'
  return session.parent_session_id ? 'channel' : 'workspace'
}
