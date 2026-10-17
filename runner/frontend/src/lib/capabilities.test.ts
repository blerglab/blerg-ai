import { describe, expect, it } from 'vitest'
import { capabilityCount, enabledCapabilityCount, latestCapabilities, matchesQuery, newerReport, normalizeCapabilities, withLiveMcp } from './capabilities'
import type { AgentEvent, AgentEventKind } from '../types'

const ev = (seq: number, kind: AgentEventKind, payload: unknown): AgentEvent => ({
  type: 'agent_event', session_id: 's1', client_event_id: `e${seq}`, seq, ts: '2026-09-26T00:00:00Z', kind, payload,
})

describe('capabilities', () => {
  it('normalizes untrusted payloads', () => {
    expect(normalizeCapabilities(null)).toBeNull()
    expect(normalizeCapabilities({ groups: 'x' })).toBeNull()
    const p = normalizeCapabilities({
      engine: 'claude', model: 7,
      groups: [
        { id: 'skills', label: 'Skills', items: [{ name: 'a', description: 3 }, { nope: true }, null, { name: '' }] },
        { id: 'skills', label: 'dup', items: [{ name: 'dup' }] },
        { id: 'evil', label: 'Evil', items: [{ name: 'x' }] },
        { id: 'tools', items: 'not-a-list', total: 'many' },
      ],
    })!
    expect(p.engine).toBe('claude')
    expect(p.model).toBeUndefined()
    expect(p.groups.map(g => g.id)).toEqual(['skills', 'tools'])
    expect(p.groups[0].items).toEqual([{ name: 'a', description: undefined, detail: undefined, status: undefined, source: undefined }])
    expect(p.groups[1]).toMatchObject({ label: 'tools', items: [], total: undefined })
    expect(capabilityCount(p)).toBe(1)
  })

  it('finds the latest report and prefers the newer seq', () => {
    const events = [
      ev(1, 'capabilities', { engine: 'claude', groups: [{ id: 'tools', label: 'Tools', items: [{ name: 'Bash' }] }] }),
      ev(2, 'turn_done', {}),
      ev(3, 'capabilities', { engine: 'claude', groups: [{ id: 'tools', label: 'Tools', items: [{ name: 'Bash' }, { name: 'Read' }] }] }),
      ev(4, 'capabilities', 'garbage'),
    ]
    const latest = latestCapabilities(events)!
    expect(latest.seq).toBe(3)
    expect(capabilityCount(latest.payload)).toBe(2)
    expect(latestCapabilities([])).toBeNull()
    const older = { ...latest, seq: 1 }
    expect(newerReport(older, latest)).toBe(latest)
    expect(newerReport(latest, older)).toBe(latest)
    expect(newerReport(null, latest)).toBe(latest)
  })

  it('counts enabled skills and plugins but excludes disabled Codex inventory', () => {
    const payload = normalizeCapabilities({ engine: 'codex', groups: [
      { id: 'skills', items: [{ name: 'implicit' }, { name: 'active', status: 'enabled' }, { name: 'inactive', status: 'disabled' }] },
      { id: 'plugins', items: [{ name: 'active', status: 'enabled' }, { name: 'inactive', status: 'disabled' }] },
      { id: 'mcp', items: [{ name: 'online', status: 'connected' }, { name: 'offline', status: 'failed' }] },
    ] })!
    expect(enabledCapabilityCount(payload)).toBe(4)
  })

  it('matches a query against any shown field', () => {
    const it1 = { name: 'pdf', description: 'Read PDFs', source: 'plugin', status: 'enabled', detail: 'v1' }
    expect(matchesQuery(it1, '')).toBe(true)
    expect(matchesQuery(it1, 'PLUG')).toBe(true)
    expect(matchesQuery(it1, 'read')).toBe(true)
    expect(matchesQuery(it1, 'zzz')).toBe(false)
  })
})

it('keeps only declared adapter tool-schema refresh policies', () => {
  for (const policy of ['new_session', 'next_turn']) {
    expect(normalizeCapabilities({ groups: [], tool_schema_refresh: policy })?.tool_schema_refresh).toBe(policy)
  }
  for (const policy of ['immediate', 'NEXT_TURN', { secret: 'untrusted' }, null]) {
    expect(normalizeCapabilities({ groups: [], tool_schema_refresh: policy })?.tool_schema_refresh).toBeUndefined()
  }
})

describe('withLiveMcp', () => {
  const base = { engine: 'claude', groups: [{ id: 'mcp' as const, label: 'MCP servers', items: [{ name: 'a', status: 'pending', source: 'user' }, { name: 'b', status: 'connected' }] }] } as never
  const live = (servers: { name: string; status: string; source: string; tools: number }[]) => ({ servers, settled: true, since: null, engine: 'claude' }) as never

  it('reads each server from the live state and keeps one it does not name', () => {
    const out = withLiveMcp(base, live([{ name: 'a', status: 'connected', source: 'user', tools: 3 }, { name: 'c', status: 'failed', source: '', tools: 0 }]))
    const items = out.groups[0].items
    expect(items.map(i => [i.name, i.status, i.detail])).toEqual([['a', 'connected', '3 tools'], ['c', 'failed', undefined], ['b', 'connected', undefined]])
  })

  it('is the payload itself when the live state has no servers', () => {
    expect(withLiveMcp(base, live([]))).toBe(base)
    expect(withLiveMcp(base, null)).toBe(base)
  })
})
