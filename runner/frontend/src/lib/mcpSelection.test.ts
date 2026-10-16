import { describe, it, expect } from 'vitest'
import { buildSelection, presetModes } from './mcpSelection'
import type { McpToolInfo } from '../types'

const ro: McpToolInfo = { name: 'read', description: 'reads', hash: 'h-read', annotations: { readOnlyHint: true } }

describe('mcpSelection', () => {
  it('turns an unreviewable tool off under every preset', () => {
    const unsafe = { ...ro, unsafe: 'hidden_chars' }
    for (const p of ['allow_all', 'approval', 'none'] as const) expect(presetModes(p, [unsafe])).toEqual({ read: 'off' })
  })

  it('skips an unreviewable tool and carries the claim only when set', () => {
    const tools = { c1: { status: 'ready' as const, tools: [{ ...ro, unsafe: 'nul' }, { name: 'send', description: 's', hash: 'h-send' }] } }
    const modes = { c1: { read: 'allow' as const, send: 'propose' as const } }
    expect(buildSelection(new Set(['c1']), modes, tools, {}, { c1: 'approval' }))
      .toEqual([{ connection: 'c1', tools: { send: { mode: 'propose', hash: 'h-send' } }, preset: 'approval' }])
    expect(buildSelection(new Set(['c1']), modes, tools, {})[0]).not.toHaveProperty('preset')
  })
})
