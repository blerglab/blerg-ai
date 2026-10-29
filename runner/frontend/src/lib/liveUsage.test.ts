import { describe, expect, it } from 'vitest'
import { compactTokens, contextMeter, readContext, readParts, readUsage, subagentChip, usageChip, withContextNote } from './liveUsage'

describe('the Running now token chip', () => {
  it('reads the server usage and refuses malformed figures', () => {
    expect(readUsage({ total: 30, input: 10, output: 5, cache_read: 10, cache_write: 5, subagents: 3, turns: 2, turn: { tokens: 6, input: 1, output: 2, cache_read: 3, cache_write: 0, in_flight: true }, cost_usd: 0.5 }))
      .toEqual({ total: 30, input: 10, output: 5, cache_read: 10, cache_write: 5, subagents: 3, turns: 2, turn: { tokens: 6, input: 1, output: 2, cache_read: 3, cache_write: 0, in_flight: true }, cost_usd: 0.5 })
    expect(readUsage(null)).toBeNull()
    expect(readUsage({ total: -1, input: 0, output: 0, cache_read: 0, cache_write: 0 })).toBeNull()
    expect(readUsage({ total: 1, input: '1', output: 0, cache_read: 0, cache_write: 0 })).toBeNull()
    expect(readUsage({ total: 1, input: 1, output: 0, cache_read: 0, cache_write: 0, turn: { tokens: 'x' } })?.turn).toBeUndefined()
  })

  it('is compact, with the turn, input against cached and the cost in its tooltip', () => {
    expect(compactTokens(950)).toBe('950')
    expect(compactTokens(180_000)).toBe('180k')
    expect(compactTokens(1_412_000)).toBe('1.4M')
    const chip = usageChip({ total: 1_412_000, input: 12_000, output: 45_000, cache_read: 1_355_000, cache_write: 0, subagents: 0, turns: 4,
      turn: { tokens: 40_000, input: 1000, output: 2000, cache_read: 37_000, cache_write: 0, in_flight: true }, cost_usd: 3 })
    expect(chip.text).toBe('1.4M')
    expect(chip.label.split('\n')).toEqual([
      'Tokens so far: 1.4M tokens', 'This turn so far: 40k tokens', 'Input 12k · cached 1.4M · output 45k', 'Estimated cost: $3.00',
    ])
    expect(usageChip(null)).toEqual({ text: '–', label: expect.stringMatching(/none recorded yet/) })
  })

  it('gives a subagent the same breakdown, from the server parts or an event usage, and says when it has none', () => {
    expect(readParts({ input: 1, output: 2, cache_read: 3, cache_write: 4 })).toEqual({ input: 1, output: 2, cache_read: 3, cache_write: 4 })
    expect(readParts({ input_tokens: 1, output_tokens: 2, cache_read_tokens: 3, cache_write_tokens: 0 })).toEqual({ input: 1, output: 2, cache_read: 3, cache_write: 0 })
    expect(readParts({ output_tokens: 9 })).toEqual({ input: 0, output: 9, cache_read: 0, cache_write: 0 })
    expect(readParts({ input: -1 })).toBeNull()
    expect(readParts({})).toBeNull()
    const chip = subagentChip(205_000, { input: 5000, output: 10_000, cache_read: 190_000, cache_write: 0 }, true)
    expect(chip.text).toBe('205k')
    expect(chip.label.split('\n')).toEqual(['Tokens so far: 205k tokens', 'This run so far: 205k tokens', 'Input 5.0k · cached 190k · output 10k'])
    expect(subagentChip(205_000, null, true).label).toBe('Tokens so far: 205k tokens\nBreakdown unavailable: only this subagent\'s total was reported')
    expect(subagentChip(null, null, true).text).toBe('–')
  })
})

describe('the context meter (#191)', () => {
  it('reads the server reading and refuses a malformed one', () => {
    expect(readContext({ tokens: 124_000, window: 200_000, at: '2026-10-26T12:00:00Z' })).toEqual({ tokens: 124_000, window: 200_000, compacted: false })
    expect(readContext({ compacted: true, compacted_from: 167_000, window: 200_000 })).toEqual({ compacted: true, compacted_from: 167_000, window: 200_000 })
    expect(readContext({ tokens: -1, window: 10 })).toBeNull()
    expect(readContext({ tokens: '5', window: 10 })).toBeNull()
    expect(readContext({ window: 200_000 })).toBeNull()
    expect(readContext(null)).toBeNull()
    expect(readUsage({ total: 1, input: 1, output: 0, cache_read: 0, cache_write: 0, context: { tokens: 5, window: 10 } })?.context).toEqual({ tokens: 5, window: 10, compacted: false })
    expect(readUsage({ total: 1, input: 1, output: 0, cache_read: 0, cache_write: 0, context: { tokens: 'x' } })?.context).toBeUndefined()
  })

  it('shows the share of the window, the threshold or that the engine does not report one', () => {
    const m = contextMeter({ tokens: 124_000, window: 200_000, compacted: false }, 'Claude Code')!
    expect(m).toMatchObject({ percent: 62, text: '62%', warn: false, compacted: false })
    expect(m.label.split('\n')).toEqual(['62% context: 124k of 200k tokens', 'Compaction threshold: not reported by Claude Code', 'As of the end of its latest turn'])
    const native = contextMeter({ tokens: 120_000, window: 200_000, compact_at: 160_000, compacted: false }, 'Blerg')!
    expect(native.label).toContain('Blerg compacts at 160k (80%)')
  })

  it('warns quietly from 85% of the threshold, or of the window when there is none', () => {
    expect(contextMeter({ tokens: 169_000, window: 200_000, compacted: false }, 'Codex')!.warn).toBe(false)
    const near = contextMeter({ tokens: 170_000, window: 200_000, compacted: false }, 'Codex')!
    expect(near.warn).toBe(true)
    expect(near.label).toContain('Near compaction: Codex will summarize the conversation soon')
    expect(contextMeter({ tokens: 136_000, window: 200_000, compact_at: 160_000, compacted: false }, 'Blerg')!.warn).toBe(true)
    expect(contextMeter({ tokens: 300_000, window: 200_000, compacted: false }, 'Codex')!.percent).toBe(100)
  })

  it('marks a compaction, with or without a size since', () => {
    const after = contextMeter({ tokens: 30_000, window: 200_000, compacted: true, compacted_from: 170_000 }, 'Claude Code')!
    expect(after).toMatchObject({ percent: 15, compacted: true })
    expect(after.label).toContain('Compacted in its latest turn (from 170k tokens)')
    const only = contextMeter({ window: 200_000, compacted: true }, 'Codex')!
    expect(only).toMatchObject({ percent: null, text: '', compacted: true })
    expect(only.label.split('\n')[0]).toBe('Context compacted: its size since then is known after the next turn')
  })

  it('hides the meter when the size or the window is unknown, and the token chip says so', () => {
    expect(contextMeter(undefined, 'Hermes')).toBeNull()
    expect(contextMeter({ tokens: 50_000, compacted: false }, 'Claude Code')).toBeNull()
    const usage = readUsage({ total: 10, input: 5, output: 5, cache_read: 0, cache_write: 0, turns: 2 })!
    expect(withContextNote(usageChip(usage), usage, null, 'Hermes').label).toMatch(/\nContext: not reported by Hermes$/)
    const m = contextMeter({ tokens: 5, window: 10, compacted: false }, 'Codex')
    expect(withContextNote(usageChip(usage), usage, m, 'Codex').label).not.toMatch(/Context/)
    expect(withContextNote(usageChip(null), null, null, 'Codex').label).not.toMatch(/Context/)
  })
})
