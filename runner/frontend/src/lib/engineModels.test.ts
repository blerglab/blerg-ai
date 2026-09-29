import { describe, it, expect } from 'vitest'
import {
  BUILTIN_MODELS,
  builtinModels,
  defaultModel,
  effectiveEffort,
  isModelIdFor,
  hasAutoEffort,
  modelDefaultEffort,
  parseModelList,
  parseModelSource,
} from './engineModels'

const CLAUDE = BUILTIN_MODELS.claude

describe('parseModelList', () => {
  it('keeps valid entries, main first, and drops junk efforts', () => {
    const out = parseModelList('claude', {
      models: [
        { id: 'claude-old-1', name: 'Old', section: 'overflow', efforts: ['high'] },
        { id: 'claude-new-1', name: 'New', section: 'main', efforts: ['max', 'max', 'bad effort', 'low'], default_effort: 'ultra' },
        { id: 'claude-new-1', name: 'Dup', section: 'main', efforts: [] },
        { id: '--model', name: 'Flag', section: 'main', efforts: [] },
        { id: 'claude-x y', name: 'Space', section: 'main', efforts: [] },
        null,
        'claude-str',
      ],
    })
    expect(out.map(m => m.id)).toEqual(['claude-new-1', 'claude-old-1'])
    expect(out[0].efforts).toEqual(['max', 'low'])
    expect(out[0].default_effort).toBeUndefined()
  })

  it("applies each engine's id rule", () => {
    expect(isModelIdFor('claude', 'gpt-5')).toBe(false)
    expect(isModelIdFor('codex', 'gpt-5-codex')).toBe(true)
    expect(isModelIdFor('hermes', 'openrouter/anthropic/claude-sonnet-4')).toBe(true)
    expect(isModelIdFor('hermes', '-m')).toBe(false)
    const out = parseModelList('codex', { models: [{ id: 'gpt-5-codex', name: 'GPT-5', section: 'main', efforts: ['ultra'] }] })
    expect(out[0].efforts).toEqual(['ultra'])
  })

  it('accepts an empty list (no picker) but rejects a garbled answer', () => {
    expect(parseModelList('codex', { models: [], source: 'none' })).toEqual([])
    expect(() => parseModelList('claude', {})).toThrow()
    expect(() => parseModelList('claude', { models: [{ id: 'nope' }] })).toThrow()
  })
})

describe('model defaults', () => {
  it('has a built-in list for claude and none for engines without one', () => {
    expect(builtinModels('claude').length).toBeGreaterThan(0)
    expect(builtinModels('')).toBe(builtinModels('claude'))
    expect(builtinModels('codex')).toEqual([])
  })

  it('defaults Claude to the Sonnet family, others to their first main model', () => {
    expect(defaultModel('claude', CLAUDE)?.id).toBe('claude-sonnet-5')
    const noSonnet = CLAUDE.filter(m => !m.id.startsWith('claude-sonnet'))
    expect(defaultModel('claude', noSonnet)?.id).toBe('claude-opus-5-5')
    expect(defaultModel('codex', [
      { id: 'b', name: 'B', description: '', section: 'overflow', efforts: [] },
      { id: 'a', name: 'A', description: '', section: 'main', efforts: [] },
    ])?.id).toBe('a')
    expect(defaultModel('codex', [])).toBeUndefined()
  })

  it("preselects the model's default effort, and none for Haiku", () => {
    const opus = CLAUDE.find(m => m.id === 'claude-opus-5-5')
    const haiku = CLAUDE.find(m => m.id === 'claude-haiku-4-5-20251001')
    expect(modelDefaultEffort(opus)).toBe('medium')
    expect(modelDefaultEffort(haiku)).toBeUndefined()
    expect(effectiveEffort(haiku, 'max')).toBeUndefined()
  })

  it('keeps an explicit effort only while the model supports it', () => {
    const o46 = CLAUDE.find(m => m.id === 'claude-opus-4-6')
    expect(effectiveEffort(o46, 'max')).toBe('max')
    expect(effectiveEffort(o46, 'xhigh')).toBe('high')
    expect(effectiveEffort(o46, null)).toBe('high')
  })
})

describe('daemon-reported lists (Codex, Hermes)', () => {
  const QWEN = {
    id: 'qwen3-30b', name: 'qwen3-30b', description: '', section: 'main' as const,
    efforts: ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max', 'ultra'], effort_kind: 'reasoning',
  }

  it('reads the source, and anything unknown as none', () => {
    expect(parseModelSource({ source: 'daemon', models: [] })).toBe('daemon')
    expect(parseModelSource({ source: 'live', models: [] })).toBe('live')
    expect(parseModelSource({ source: 'other' })).toBe('none')
    expect(parseModelSource(null)).toBe('none')
  })

  it('offers Auto (no effort) for a model with levels but no default of its own', () => {
    expect(hasAutoEffort(QWEN)).toBe(true)
    expect(modelDefaultEffort(QWEN)).toBeUndefined()
    expect(effectiveEffort(QWEN, null)).toBeUndefined()
    expect(effectiveEffort(QWEN, '')).toBeUndefined()
    expect(effectiveEffort(QWEN, 'minimal')).toBe('minimal')
    // Claude models always carry a default: never Auto.
    expect(CLAUDE.some(m => hasAutoEffort(m))).toBe(false)
    expect(hasAutoEffort({ ...QWEN, efforts: [] })).toBe(false)
  })

  it('keeps a hostile id out of a daemon list with the generic rule', () => {
    const list = parseModelList('hermes', { source: 'daemon', models: [
      QWEN, { ...QWEN, id: '--yolo' }, { ...QWEN, id: 'a b' }, { ...QWEN, id: 'org/model:tag' },
    ] })
    expect(list.map(m => m.id)).toEqual(['qwen3-30b', 'org/model:tag'])
    expect(list[0].default_effort).toBeUndefined()
  })
})
