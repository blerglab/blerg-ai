import { describe, expect, it } from 'vitest'
import { capabilityCount, latestCapabilities, matchesQuery, newerReport, normalizeCapabilities } from './capabilities'
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

  it('matches a query against any shown field', () => {
    const it1 = { name: 'pdf', description: 'Read PDFs', source: 'plugin', status: 'enabled', detail: 'v1' }
    expect(matchesQuery(it1, '')).toBe(true)
    expect(matchesQuery(it1, 'PLUG')).toBe(true)
    expect(matchesQuery(it1, 'read')).toBe(true)
    expect(matchesQuery(it1, 'zzz')).toBe(false)
  })
})
