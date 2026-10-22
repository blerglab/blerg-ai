// Capabilities reports (agent_event kind "capabilities"): what a session has
// loaded. The server already sanitizes them; this still treats the payload
// as untrusted JSON so a malformed one renders as "nothing" instead of
// crashing the chat view.
import { modelDisplayName, type McpLive } from '@blerglab/chat'
import type { AgentEvent, CapabilitiesPayload, CapabilityGroup, CapabilityGroupId, CapabilityItem } from '../types'

export const CAPABILITIES_KIND = 'capabilities'

// Every group a report may carry, in display order, with the label used when
// the engine doesn't report that group at all.
export const CAPABILITY_GROUPS: { id: CapabilityGroupId; label: string }[] = [
  { id: 'skills', label: 'Skills' },
  { id: 'plugins', label: 'Plugins' },
  { id: 'mcp', label: 'MCP servers' },
  { id: 'commands', label: 'Slash commands' },
  { id: 'tools', label: 'Tools' },
  { id: 'agents', label: 'Subagents' },
]
const GROUP_IDS = new Set<string>(CAPABILITY_GROUPS.map(g => g.id))

export interface CapabilitiesReport {
  payload: CapabilitiesPayload
  seq: number
  ts: string
}

const str = (v: unknown): string | undefined => (typeof v === 'string' && v !== '' ? v : undefined)

function item(raw: unknown): CapabilityItem | null {
  if (!raw || typeof raw !== 'object') return null
  const o = raw as Record<string, unknown>
  const name = str(o.name)
  if (!name) return null
  return { name, description: str(o.description), detail: str(o.detail), status: str(o.status), source: str(o.source) }
}

// normalizeCapabilities returns a well-typed payload, or null when raw isn't
// one. Unknown groups and nameless items are dropped.
export function normalizeCapabilities(raw: unknown): CapabilitiesPayload | null {
  if (!raw || typeof raw !== 'object') return null
  const o = raw as Record<string, unknown>
  if (!Array.isArray(o.groups)) return null
  const groups: CapabilityGroup[] = []
  const seen = new Set<string>()
  for (const g of o.groups as unknown[]) {
    if (!g || typeof g !== 'object') continue
    const go = g as Record<string, unknown>
    const id = str(go.id)
    if (!id || !GROUP_IDS.has(id) || seen.has(id)) continue
    seen.add(id)
    const items = Array.isArray(go.items) ? (go.items as unknown[]).map(item).filter((x): x is CapabilityItem => x !== null) : []
    groups.push({
      id: id as CapabilityGroupId,
      label: str(go.label) ?? id,
      note: str(go.note),
      total: typeof go.total === 'number' && Number.isFinite(go.total) ? go.total : undefined,
      items,
    })
  }
  return {
    engine: str(o.engine) ?? '',
    tool_schema_refresh: o.tool_schema_refresh === 'next_turn' || o.tool_schema_refresh === 'new_session' ? o.tool_schema_refresh : undefined,
    engine_name: str(o.engine_name),
    model: str(o.model),
    version: str(o.version),
    cwd: str(o.cwd),
    permission_mode: str(o.permission_mode),
    note: str(o.note),
    groups,
  }
}

// latestCapabilities is the newest capabilities report in a transcript.
export function latestCapabilities(events: AgentEvent[]): CapabilitiesReport | null {
  for (let i = events.length - 1; i >= 0; i--) {
    const ev = events[i]
    if (ev.kind !== CAPABILITIES_KIND) continue
    const payload = normalizeCapabilities(ev.payload)
    if (payload) return { payload, seq: ev.seq ?? Number.MAX_SAFE_INTEGER, ts: ev.ts }
  }
  return null
}

// newerReport picks the more recent of two reports (by seq).
export function newerReport(a: CapabilitiesReport | null, b: CapabilitiesReport | null): CapabilitiesReport | null {
  if (!a) return b
  if (!b) return a
  return b.seq > a.seq ? b : a
}

export function capabilityCount(p: CapabilitiesPayload | null | undefined): number {
  return p ? p.groups.reduce((n, g) => n + g.items.length, 0) : 0
}

// matchesQuery: case-insensitive match on anything the panel shows.
export function matchesQuery(it: CapabilityItem, q: string): boolean {
  if (!q) return true
  const needle = q.toLowerCase()
  return [it.name, it.description, it.detail, it.status, it.source].some(v => v?.toLowerCase().includes(needle))
}

// latestCapabilitiesEvent is the newest capabilities event in a transcript (the event itself, so a
// store selector gets a stable reference), or undefined.
export function latestCapabilitiesEvent(events: AgentEvent[] | undefined): AgentEvent | undefined {
  if (!events) return undefined
  for (let i = events.length - 1; i >= 0; i--) if (events[i].kind === CAPABILITIES_KIND) return events[i]
  return undefined
}

// enabledCapabilityCount: what the session has switched on, for the Capabilities tab's badge: its
// skills, its plugins that are enabled (or carry no status), and its MCP servers that are connected.
// Slash commands, tools and subagents are what those bring, not more of them.
export function enabledCapabilityCount(p: CapabilitiesPayload | null | undefined): number {
  if (!p) return 0
  let n = 0
  for (const g of p.groups) {
    if (g.id === 'skills' || g.id === 'plugins') n += g.items.filter(i => !i.status || i.status === 'enabled').length
    else if (g.id === 'mcp') n += g.items.filter(i => i.status === 'connected').length
  }
  return n
}

const MCP_RELEASED_NOTES: Record<string, string> = {
  settled: 'The first message was sent once the servers were ready.',
  timeout: 'The first message was sent after the wait ran out.',
  person: 'The first message was sent when you chose Send now.',
}

/** The words a status chip says ("needs-auth" reads "needs sign-in"; "pending" is "connecting"). */
export function statusWord(status: string): string {
  return status === 'needs-auth' ? 'needs sign-in' : status === 'pending' ? 'connecting' : status === 'lazy' ? 'connects on demand' : status
}

/** withLiveMcp: the payload with its MCP servers group read from the live state (each server's status
 *  now), or the payload as it is when the live state has no servers. A server the live state does not
 *  name keeps its start-up row. */
export function withLiveMcp(payload: CapabilitiesPayload, live: McpLive | null | undefined): CapabilitiesPayload {
  if (!live || live.servers.length === 0) return payload
  const old = payload.groups.find(g => g.id === 'mcp')
  const snap = new Map((old?.items ?? []).map(i => [i.name, i]))
  const items: CapabilityItem[] = live.servers.map(sv => {
    const was = snap.get(sv.name)
    return {
      ...was,
      name: sv.name,
      status: sv.status === 'unknown' ? was?.status : sv.status,
      source: sv.source || was?.source,
      detail: sv.status === 'connected' && sv.tools > 0 ? `${sv.tools} ${sv.tools === 1 ? 'tool' : 'tools'}` : was?.detail,
    }
  })
  const names = new Set(items.map(i => i.name))
  for (const i of old?.items ?? []) if (!names.has(i.name)) items.push(i)
  const note = live.released ? MCP_RELEASED_NOTES[live.released] : old?.note
  const group: CapabilityGroup = { id: 'mcp', label: old?.label ?? 'MCP servers', note, total: old?.total, items }
  return { ...payload, groups: old ? payload.groups.map(g => (g.id === 'mcp' ? group : g)) : [...payload.groups, group] }
}

// ─── The Capabilities header card ────────────────────────────────────────────

/** A permission mode in words, and whether it lets the agent act without asking (drawn amber). The
 *  known values of each engine are mapped; anything else shows as reported, neutral. */
const PERMISSION_MODES: Record<string, { label: string; risky?: boolean }> = {
  // Claude Code
  default: { label: 'Default' },
  acceptEdits: { label: 'Accept edits' },
  plan: { label: 'Plan' },
  auto: { label: 'Auto' },
  dontAsk: { label: 'Don’t ask' },
  bypassPermissions: { label: 'Bypass permissions', risky: true },
  // Codex approval policies and sandboxes
  never: { label: 'Never asks', risky: true },
  'on-request': { label: 'Asks on request' },
  'on-failure': { label: 'Asks on failure' },
  untrusted: { label: 'Asks for untrusted' },
  restricted: { label: 'Restricted' },
  'read-only': { label: 'Read only' },
  'workspace-write': { label: 'Workspace write' },
  'danger-full-access': { label: 'Full access', risky: true },
}

export function permissionLabel(mode: string): { label: string; risky: boolean } {
  const known = Object.prototype.hasOwnProperty.call(PERMISSION_MODES, mode) ? PERMISSION_MODES[mode] : undefined
  return known ? { label: known.label, risky: known.risky === true } : { label: mode, risky: false }
}

const CLAUDE_FAMILY = /^claude-(opus|sonnet|haiku|fable)-(\d+)(?:-(\d{1,2}))?(?:-\d{8})?(\[1m\])?$/

/** A model id as a person reads it: "claude-opus-5-5" is "Opus 5.5". The catalog's name when this
 *  build knows the id, then the Claude family pattern, then the shared chat package's display name. */
export function humanModelName(id: string, catalog: readonly { id: string; name: string }[] = []): string {
  const listed = catalog.find(m => m.id === id)?.name
  if (listed) return listed
  const m = CLAUDE_FAMILY.exec(id)
  if (m) return `${m[1][0].toUpperCase()}${m[1].slice(1)} ${m[2]}${m[3] ? `.${m[3]}` : ''}${m[4] ? ' (1M)' : ''}`
  return modelDisplayName(id)
}

/** A long path cut in the middle, so its start (where) and its end (which folder) both show. */
export function middleTruncate(s: string, max = 24): string {
  if (s.length <= max) return s
  const keep = max - 1
  const head = Math.ceil(keep * 0.4)
  return `${s.slice(0, head)}…${s.slice(s.length - (keep - head))}`
}

/** "As of 12:05 AM" for a report from today, "As of Oct 21, 12:05 AM" for an older one, in the
 *  browser's locale as the rest of the app's times (the locale is a parameter for tests); null when
 *  the time cannot be read. */
export function asOfLabel(ts: string, now: Date = new Date(), locale?: string): string | null {
  const t = Date.parse(ts)
  if (!Number.isFinite(t)) return null
  const d = new Date(t)
  const time = d.toLocaleTimeString(locale, { hour: 'numeric', minute: '2-digit' })
  const today = d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth() && d.getDate() === now.getDate()
  if (today) return `As of ${time}`
  const sameYear = d.getFullYear() === now.getFullYear()
  const date = d.toLocaleDateString(locale, sameYear ? { month: 'short', day: 'numeric' } : { month: 'short', day: 'numeric', year: 'numeric' })
  return `As of ${date}, ${time}`
}

/** A row's status as one glyph and its tone; the word (statusWord) goes to the tooltip and a screen
 *  reader only. */
export function statusGlyph(status: string): { glyph: string; tone: 'ok' | 'off' | 'wait' | 'warn' | 'bad' | 'other' } {
  switch (status) {
    case 'enabled':
    case 'connected': return { glyph: '●', tone: 'ok' }
    case 'disabled': return { glyph: '○', tone: 'off' }
    case 'lazy': return { glyph: '◇', tone: 'off' }
    case 'pending': return { glyph: '◌', tone: 'wait' }
    case 'needs-auth': return { glyph: '!', tone: 'warn' }
    case 'failed':
    case 'error': return { glyph: '×', tone: 'bad' }
    default: return { glyph: '•', tone: 'other' }
  }
}
