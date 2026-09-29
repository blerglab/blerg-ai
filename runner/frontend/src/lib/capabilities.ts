// Capabilities reports (agent_event kind "capabilities"): what a session has
// loaded. The server already sanitizes them; this still treats the payload
// as untrusted JSON so a malformed one renders as "nothing" instead of
// crashing the chat view.
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
