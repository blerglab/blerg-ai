import { describe, expect, it } from 'vitest'
import { asOfLabel, capabilityCount, enabledCapabilityCount, humanModelName, latestCapabilities, matchesQuery, middleTruncate, newerReport, normalizeCapabilities, permissionLabel, statusGlyph, statusWord, withLiveMcp } from './capabilities'
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

it('describes on-demand MCP honestly without counting it as connected', () => {
  expect(statusWord('lazy')).toBe('connects on demand')
  const p = normalizeCapabilities({ groups: [{ id: 'mcp', items: [{ name: 'docs', status: 'lazy' }] }] })!
  expect(enabledCapabilityCount(p)).toBe(0)
})

describe('the Capabilities header card (card #132)', () => {
  it('names permission modes in words, amber only for the ones that act without asking', () => {
    expect(permissionLabel('bypassPermissions')).toEqual({ label: 'Bypass permissions', risky: true })
    expect(permissionLabel('default')).toEqual({ label: 'Default', risky: false })
    expect(permissionLabel('acceptEdits')).toEqual({ label: 'Accept edits', risky: false })
    expect(permissionLabel('plan')).toEqual({ label: 'Plan', risky: false })
    expect(permissionLabel('never')).toEqual({ label: 'Never asks', risky: true })
    expect(permissionLabel('restricted')).toEqual({ label: 'Restricted', risky: false })
    expect(permissionLabel('danger-full-access')).toEqual({ label: 'Full access', risky: true })
    // Unknown: as reported, neutral; never a prototype key.
    expect(permissionLabel('someNewMode')).toEqual({ label: 'someNewMode', risky: false })
    expect(permissionLabel('constructor')).toEqual({ label: 'constructor', risky: false })
  })

  it('gives a model its human name, the raw id staying for the tooltip', () => {
    expect(humanModelName('claude-opus-5-5')).toBe('Opus 5.5')
    expect(humanModelName('claude-sonnet-5')).toBe('Sonnet 5')
    expect(humanModelName('claude-haiku-4-5-20251001')).toBe('Haiku 4.5')
    expect(humanModelName('claude-opus-4-6[1m]')).toBe('Opus 4.6 (1M)')
    expect(humanModelName('claude-x', [{ id: 'claude-x', name: 'Catalog name' }])).toBe('Catalog name')
    expect(humanModelName('gpt-5.4-codex')).toBe('gpt-5.4-codex')
  })

  it('cuts a long path in the middle, keeping both ends', () => {
    expect(middleTruncate('~/repositories/blerg')).toBe('~/repositories/blerg')
    const cut = middleTruncate('/home/dev/repositories/a-long-workspace/services/session-store')
    expect(cut.length).toBe(24)
    expect(cut).toMatch(/^\/home\/dev.*….*session-store$/)
  })

  it('says As of with the time today, and the date before it otherwise', () => {
    const now = new Date(2026, 9, 22, 9, 30)
    const sp = (s: string | null) => s?.replace(/\s/g, ' ')
    expect(sp(asOfLabel(new Date(2026, 9, 22, 0, 5).toISOString(), now, 'en-US'))).toBe('As of 12:05 AM')
    expect(sp(asOfLabel(new Date(2026, 9, 21, 23, 5).toISOString(), now, 'en-US'))).toBe('As of Oct 21, 11:05 PM')
    expect(sp(asOfLabel(new Date(2025, 0, 3, 13, 0).toISOString(), now, 'en-US'))).toBe('As of Jan 3, 2025, 1:00 PM')
    expect(asOfLabel('', now)).toBeNull()
    // The browser's locale by default, as the rest of the app's times: a 24-hour locale reads 24-hour.
    expect(sp(asOfLabel(new Date(2026, 9, 22, 13, 5).toISOString(), now, 'en-GB'))).toBe('As of 13:05')
    expect(asOfLabel(new Date(2026, 9, 22, 13, 5).toISOString(), now)).toBe(`As of ${new Date(2026, 9, 22, 13, 5).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })}`)
  })

  it('draws a row status as one glyph with a tone', () => {
    expect(statusGlyph('enabled')).toEqual({ glyph: '●', tone: 'ok' })
    expect(statusGlyph('connected').tone).toBe('ok')
    expect(statusGlyph('disabled')).toEqual({ glyph: '○', tone: 'off' })
    expect(statusGlyph('failed')).toEqual({ glyph: '×', tone: 'bad' })
    expect(statusGlyph('error').tone).toBe('bad')
    expect(statusGlyph('needs-auth').tone).toBe('warn')
    expect(statusGlyph('whatever').tone).toBe('other')
  })
})
