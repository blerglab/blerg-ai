import { describe, expect, it } from 'vitest'
import { budgetLine, decidedLine, delegationStates, engineLabel, runtimeWords } from './delegations'
import type { AgentEvent } from '../types'

function ev(kind: string, ts: string, payload: unknown): AgentEvent {
  return { type: 'agent_event', session_id: 's1', client_event_id: `${kind}-${ts}`, ts, kind, payload } as AgentEvent
}

const clock = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })

describe('delegation states', () => {
  it('folds the decided event into its request', () => {
    const m = delegationStates([
      ev('delegation_request', '2026-10-17T15:00:00Z', { request_id: 'r1' }),
      ev('delegation_request', '2026-10-17T15:00:01Z', { request_id: 'r2' }),
      ev('delegation_decided', '2026-10-17T15:12:00Z', { request_id: 'r1', status: 'allowed', choice: 'once', child_session_id: 'c1' }),
    ])
    expect(m.get('r1')?.decided?.child_session_id).toBe('c1')
    expect(m.get('r1')?.decidedAt).toBe('2026-10-17T15:12:00Z')
    expect(m.get('r2')?.decided).toBeNull()
  })

  it('keeps a decision that arrives before its request is in the window', () => {
    const m = delegationStates([ev('delegation_decided', '2026-10-17T15:12:00Z', { request_id: 'r9', status: 'denied' })])
    expect(m.get('r9')?.decided?.status).toBe('denied')
  })

  it('formats the budget line', () => {
    const now = Date.parse('2026-10-17T12:00:00Z')
    expect(budgetLine({
      engine: 'codex', available: true,
      windows: [{ id: '5h', used_percent: 62, resets_at: '2026-10-17T16:10:00Z' }, { id: '7d', used_percent: 31 }],
    }, now)).toBe(`5h 62% used, resets ${clock('2026-10-17T16:10:00Z')} · 7d 31%`)
    expect(budgetLine({ engine: 'hermes', available: true, local: true, windows: [] }, now)).toBe('local (runs on your hardware)')
    expect(budgetLine({ engine: 'hermes', available: false, windows: [], note: 'usage unknown' }, now)).toBe('usage unknown')
    expect(budgetLine(undefined, now)).toBe('')
  })

  it('says expired when past expires_at with no decision', () => {
    expect(decidedLine({ decided: null, decidedAt: null }, true)).toBe('Expired, nobody answered')
    expect(decidedLine({ decided: { request_id: 'r', status: 'expired' }, decidedAt: 'x' }, false)).toBe('Expired, nobody answered')
  })

  it('words each decision', () => {
    const at = '2026-10-17T15:12:00Z'
    const t = clock(at)
    const line = (d: Parameters<typeof decidedLine>[0]['decided']) => decidedLine({ decided: d, decidedAt: at }, false)
    expect(line({ request_id: 'r', status: 'allowed', choice: 'once' })).toBe(`Allowed once · ${t}`)
    expect(line({ request_id: 'r', status: 'allowed', choice: 'session' })).toBe(`Allowed for this session · ${t}`)
    expect(line({ request_id: 'r', status: 'allowed', choice: 'always' })).toBe(`Always allowed · ${t}`)
    expect(line({ request_id: 'r', status: 'denied' })).toBe(`Denied · ${t}`)
    expect(decidedLine({ decided: null, decidedAt: null }, false)).toBe('')
  })

  it('names engines and runtimes', () => {
    expect([engineLabel('claude'), engineLabel('codex'), engineLabel('hermes'), engineLabel('openclaw'), engineLabel('zed')])
      .toEqual(['Claude', 'Codex', 'Hermes', 'OpenClaw', 'zed'])
    expect(runtimeWords('docker')).toBe('in the desktop sandbox')
    expect(runtimeWords('daemon')).toBe('on your desktop, unsandboxed')
  })
})
