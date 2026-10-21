import { describe, expect, it } from 'vitest'
import { NO_REASONING, nextReasoning, reasoningLabel } from './reasoning'
import { ev } from '../test/chat'

describe('live reasoning metadata', () => {
  const event = (kind: string, tokens?: number, parent_call_id = '') => ev({ kind, ts: '2026-10-01T00:00:00Z', transient: true, payload: { tokens, parent_call_id } })
  it('uses native counters, handles duplicates, rejects estimates and invalid counts', () => {
    const open = nextReasoning(NO_REASONING, event('thinking_started'))
    const counted = nextReasoning(open, event('thinking_progress', 12))
    expect(nextReasoning(counted, event('thinking_progress', 5))).toBe(counted)
    for (const n of [-1, NaN, Infinity, 1.5]) expect(nextReasoning(counted, event('thinking_progress', n))).toBe(counted)
    expect(reasoningLabel(counted[''], 0)).toBe('Thinking… 12 tokens')
  })
  it('keeps scopes separate and remembers a done when the API snapshot is older', () => {
    const root = nextReasoning(NO_REASONING, event('thinking_started'))
    const child = nextReasoning(root, event('thinking_progress', 20, 'child'))
    const done = nextReasoning(child, event('thinking_done', undefined, 'child'))
    expect(done[''].active).not.toBe(false)
    expect(done.child.active).toBe(false)
    expect(nextReasoning(done, event('thinking_progress', 30, 'child'))).toBe(done)
    expect(nextReasoning(done, event('thinking_started', undefined, 'child')).child.tokens).toBeNull()
  })
  it('bounds abandoned scopes and does not replay old thinking starts as live', () => {
    const replay = { ...event('thinking_started'), transient: false }
    expect(nextReasoning(NO_REASONING, replay)).toBe(NO_REASONING)
    let scopes = NO_REASONING
    for (let i = 0; i < 300; i++) scopes = nextReasoning(scopes, event('thinking_started', undefined, `scope-${i}`))
    expect(Object.keys(scopes)).toHaveLength(256)
  })
})
