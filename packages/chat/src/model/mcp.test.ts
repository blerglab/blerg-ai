import { describe, expect, it } from 'vitest'
import type { AgentEvent } from '../types'
import { holdClock, mcpFromEvents, mcpSummary, readMcp, readMcpHold } from './mcp'

const payload = (servers: unknown[], extra: Record<string, unknown> = {}) => ({
  servers, settled: false, since: '2026-10-17T10:00:00Z', engine: 'claude', ...extra,
})
const srv = (name: string, status: string, tools = 0) => ({ name, status, source: 'user', tools })

describe('reading mcp_status', () => {
  it('checks every field and coerces an unknown status', () => {
    const m = readMcp(payload([srv('a', 'connected', 4), { name: 'b', status: 'weird', source: 7, tools: 'x' }, { status: 'failed' }, 3], { released: 'person', settled: true }))
    expect(m?.servers).toEqual([
      { name: 'a', status: 'connected', source: 'user', tools: 4 },
      { name: 'b', status: 'unknown', source: '', tools: 0 },
    ])
    expect(m?.settled).toBe(true)
    expect(m?.released).toBe('person')
    expect(m?.engine).toBe('claude')
    expect(readMcp(payload([], { released: 'bogus' }))?.released).toBeUndefined()
    expect(readMcp({ servers: 'x' })).toBeNull()
    expect(readMcp(null)).toBeNull()
  })

  it('reads the hold; a past or malformed one is none', () => {
    expect(readMcpHold({ since: '2026-10-17T10:00:00Z', until: '2026-10-17T10:00:30Z' })).toEqual({ since: Date.parse('2026-10-17T10:00:00Z'), until: Date.parse('2026-10-17T10:00:30Z') })
    expect(readMcpHold({ since: 'x', until: 'y' })).toBeNull()
    expect(readMcpHold(undefined)).toBeNull()
  })

  it('the newest snapshot of the events held, its hold only while unreleased', () => {
    const e = (n: number, p: unknown): AgentEvent => ({ type: 'agent_event', session_id: 's', client_event_id: `e${n}`, seq: n, ts: '2026-10-17T10:00:00Z', kind: 'mcp_status', payload: p })
    const held = payload([srv('a', 'pending')], { hold: { since: '2026-10-17T10:00:00Z', until: '2026-10-17T10:00:30Z' } })
    const a = mcpFromEvents([e(1, held)])
    expect(a.mcp?.servers[0].status).toBe('pending')
    expect(a.hold?.until).toBe(Date.parse('2026-10-17T10:00:30Z'))
    const b = mcpFromEvents([e(1, held), e(2, payload([srv('a', 'connected')], { settled: true, released: 'settled' }))])
    expect(b.mcp?.released).toBe('settled')
    expect(b.hold).toBeNull()
    expect(mcpFromEvents([])).toEqual({ mcp: null, hold: null })
  })
})

describe('the panel line', () => {
  const m = (servers: unknown[], settled: boolean) => readMcp(payload(servers, { settled }))!
  it('counts the servers that have answered while it connects', () => {
    expect(mcpSummary(m([srv('a', 'connected'), srv('b', 'pending'), srv('c', 'failed')], false))).toEqual({ text: 'Connecting 2 of 3…', connecting: true, warn: false })
  })
  it('says what settled, warnings last', () => {
    expect(mcpSummary(m([srv('a', 'connected'), srv('b', 'connected'), srv('c', 'connected')], true))).toEqual({ text: '3 connected', connecting: false, warn: false })
    expect(mcpSummary(m([srv('a', 'connected'), srv('b', 'connected'), srv('c', 'failed'), srv('d', 'needs-auth')], true)))
      .toEqual({ text: '2 connected · 1 failed · 1 needs sign-in', connecting: false, warn: true })
    expect(mcpSummary(m([srv('a', 'failed')], true))!.text).toBe('1 failed')
    expect(mcpSummary(m([srv('a', 'disabled')], true))!.text).toBe('1 disabled')
  })
  it('a settled snapshot never reads as connecting even if a row is pending', () => {
    expect(mcpSummary(m([srv('a', 'pending')], true))!.connecting).toBe(false)
  })
  it('no servers: nothing to say', () => {
    expect(mcpSummary(m([], false))).toBeNull()
  })
})

it('keeps on-demand servers distinct from connected, pending and future unknown states', () => {
  const m = readMcp(payload([srv('docs', 'lazy'), srv('issues', 'connected'), srv('future', 'future-state')], { settled: true }))!
  expect(m.servers.map(s => s.status)).toEqual(['lazy', 'connected', 'unknown'])
  expect(mcpSummary(m)).toEqual({ text: '1 connected · 1 on demand · 1 unknown', connecting: false, warn: false })
  expect(mcpFromEvents([{ type: 'agent_event', session_id: 's', client_event_id: 'e-lazy', seq: 1,
    ts: '2026-10-17T10:00:00Z', kind: 'mcp_status', payload: payload([srv('docs', 'lazy')], { settled: true }) }]).hold).toBeNull()
})

describe('the hold clock', () => {
  it('is elapsed over the wait, m:ss, clamped', () => {
    const hold = { since: 1000, until: 31_000 }
    expect(holdClock(hold, 13_000)).toBe('0:12 / 0:30')
    expect(holdClock(hold, 500)).toBe('0:00 / 0:30')
    expect(holdClock(hold, 99_000)).toBe('0:30 / 0:30')
  })
})
