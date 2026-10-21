// Who started a session when it was not the person in the app (SessionInfo.started_by): a tool
// holding an agent token, a cron, or the operator key. Such sessions are the person's to see
// and open, but not the kind they sit down and drive, so the sidebar keeps them in their own
// Automated section.
import type { SessionInfo } from '../types'

export function isAutomated(s: Pick<SessionInfo, 'started_by' | 'cron_id' | 'parent_session_id'>, listedIds?: ReadonlySet<string>): boolean {
  if (s.started_by?.kind === 'session' && s.parent_session_id && listedIds?.has(s.parent_session_id)) return false
  return !!s.started_by || !!s.cron_id
}

const ENGINE_NAMES: Record<string, string> = { '': 'Claude', claude: 'Claude', codex: 'Codex', hermes: 'Hermes', openclaw: 'OpenClaw' }

/** The source to group and label by: the token's name, else what kind of thing started it. A
 *  child reads "<who> from <where>": the parent's agent (its engine, when the parent is known)
 *  from the parent session. */
export function startedByLabel(s: Pick<SessionInfo, 'started_by' | 'cron_id'>, parent?: Pick<SessionInfo, 'engine'> | null): string {
  const by = s.started_by
  if (by?.kind === 'session') {
    const who = parent ? `${ENGINE_NAMES[parent.engine ?? ''] ?? parent.engine} agent` : 'an agent'
    return `${who} from ${by.name || 'another session'}`
  }
  if (by?.name) return by.name
  const kind = by?.kind ?? (s.cron_id ? 'cron' : '')
  switch (kind) {
    case 'cron': return 'cron'
    case 'agent': return 'agent token'
    case 'runner_key': return 'operator key'
    default: return kind || 'automated'
  }
}
