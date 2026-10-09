// Who started a session when it was not the person in the app (SessionInfo.started_by): a tool
// holding an agent token, a cron, or the operator key. Such sessions are the person's to see
// and open, but not the kind they sit down and drive, so the sidebar keeps them in their own
// Automated section.
import type { SessionInfo } from '../types'

export function isAutomated(s: Pick<SessionInfo, 'started_by' | 'cron_id'>): boolean {
  return !!s.started_by || !!s.cron_id
}

/** The source to group and label by: the token's name, else what kind of thing started it. */
export function startedByLabel(s: Pick<SessionInfo, 'started_by' | 'cron_id'>): string {
  const by = s.started_by
  if (by?.name) return by.name
  const kind = by?.kind ?? (s.cron_id ? 'cron' : '')
  switch (kind) {
    case 'cron': return 'cron'
    case 'agent': return 'agent token'
    case 'runner_key': return 'operator key'
    default: return kind || 'automated'
  }
}
