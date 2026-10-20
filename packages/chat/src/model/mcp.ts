// A session's MCP servers as the agent's engine reports them (event kind `mcp_status`, and the
// live state's `mcp` / `mcp_hold`): read defensively (an event is untrusted data), the words the
// panel line says, and the clock of the held first prompt. Pure.
import type { AgentEvent } from '../types'
import { parseTs } from './timeLabel'

export type McpServerStatus = 'pending' | 'connected' | 'failed' | 'needs-auth' | 'disabled' | 'lazy' | 'unknown'
export interface McpServer { name: string; status: McpServerStatus; source: string; tools: number }
export type McpReleased = 'settled' | 'timeout' | 'person'
export interface McpLive {
  servers: McpServer[]
  /** The engine has finished connecting: every server has answered, or the wait ran out. */
  settled: boolean
  since: number | null
  engine: string
  /** Why a held first prompt was let go (set on the snapshot that ended the hold). */
  released?: McpReleased
}
/** The first prompt is held while servers connect: server-time ms. */
export interface McpHold { since: number; until: number }

const STATUSES: readonly McpServerStatus[] = ['pending', 'connected', 'failed', 'needs-auth', 'disabled', 'lazy', 'unknown']
type Rec = Record<string, unknown>
const rec = (v: unknown): Rec | null => (typeof v === 'object' && v !== null && !Array.isArray(v) ? v as Rec : null)
const str = (v: unknown): string => (typeof v === 'string' ? v : '')

/** readMcp: an mcp_status payload (or the live state's `mcp`), checked; null when it is not one. */
export function readMcp(payload: unknown): McpLive | null {
  const p = rec(payload)
  if (!p || !Array.isArray(p.servers)) return null
  const servers: McpServer[] = []
  for (const s of p.servers) {
    const r = rec(s)
    if (!r || !str(r.name)) continue
    const status = STATUSES.find(x => x === r.status) ?? 'unknown'
    servers.push({ name: str(r.name), status, source: str(r.source), tools: typeof r.tools === 'number' && Number.isFinite(r.tools) ? r.tools : 0 })
  }
  const released = (['settled', 'timeout', 'person'] as const).find(x => x === p.released)
  return { servers, settled: p.settled === true, since: parseTs(str(p.since)), engine: str(p.engine), ...(released ? { released } : {}) }
}

/** readMcpHold: `{since, until}`; null when either time is missing. */
export function readMcpHold(v: unknown): McpHold | null {
  const h = rec(v)
  const since = h ? parseTs(str(h.since)) : null
  const until = h ? parseTs(str(h.until)) : null
  return since !== null && until !== null ? { since, until } : null
}

/** mcpFromEvents: the newest snapshot of the events held (the chat's own reading, on a server without
 *  the live state's); its hold is the snapshot's, which an end of the hold leaves out. */
export function mcpFromEvents(events: readonly AgentEvent[]): { mcp: McpLive | null; hold: McpHold | null } {
  for (let i = events.length - 1; i >= 0; i--) {
    if (events[i].kind !== 'mcp_status') continue
    const mcp = readMcp(events[i].payload)
    if (!mcp) continue
    return { mcp, hold: mcp.settled ? null : readMcpHold(rec(events[i].payload)?.hold) }
  }
  return { mcp: null, hold: null }
}

export interface McpSummary { text: string; connecting: boolean; warn: boolean }

const WARN: McpServerStatus[] = ['failed', 'needs-auth']

/** mcpSummary: the panel's one line, e.g. "Connecting 2 of 3…" or "2 connected · 1 failed · 1 needs
 *  sign-in". null when the session has no MCP servers. */
export function mcpSummary(m: McpLive): McpSummary | null {
  const n = m.servers.length
  if (n === 0) return null
  const count = (st: McpServerStatus) => m.servers.filter(s => s.status === st).length
  const answered = m.servers.filter(s => s.status !== 'pending').length
  const warn = m.servers.some(s => WARN.includes(s.status))
  if (!m.settled && answered < n) return { text: `Connecting ${answered} of ${n}…`, connecting: true, warn: false }
  const parts: string[] = []
  const add = (st: McpServerStatus, word: string) => { if (count(st)) parts.push(`${count(st)} ${word}`) }
  add('connected', 'connected')
  add('lazy', 'on demand')
  add('failed', 'failed')
  add('needs-auth', 'needs sign-in')
  add('disabled', 'disabled')
  add('unknown', 'unknown')
  add('pending', 'not connected')
  return { text: parts.join(' · '), connecting: false, warn }
}

const clock = (ms: number) => {
  const s = Math.max(0, Math.round(ms / 1000))
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}

/** holdClock: "0:12 / 0:30" — how long the first prompt has waited, of the most it will. now is the
 *  server's time (ms). */
export function holdClock(hold: McpHold, now: number): string {
  const total = hold.until - hold.since
  return `${clock(Math.min(Math.max(0, now - hold.since), total))} / ${clock(total)}`
}
