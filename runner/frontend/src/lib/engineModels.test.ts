import { describe, it, expect, vi, afterEach } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import {
  useEngineModels,
  modelCatalogTarget,
  type FetchedModelList,
  type ModelListCache,
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

describe('catalog targets', () => {
  it('scopes catalogs by runtime without an engine-specific frontend registry', () => {
    expect(modelCatalogTarget('cluster', 'synthetic-pod')).toEqual({ runtime: 'cluster' })
    expect(modelCatalogTarget('docker', 'workstation')).toEqual({ daemonId: 'workstation' })
    expect(modelCatalogTarget('daemon', 'other')).toEqual({ daemonId: 'other' })
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

describe('useEngineModels with a cache', () => {
  afterEach(() => vi.useRealTimers())
  const CACHED: FetchedModelList = {
    models: [{ id: 'gpt-cached', name: 'GPT cached', description: '', section: 'main', efforts: [] }],
    source: 'daemon',
    engineEfforts: [],
  }
  function memo(): ModelListCache & { store: Map<string, FetchedModelList> } {
    const store = new Map<string, FetchedModelList>()
    return {
      store,
      get: (e, d) => store.get(`${e}\n${d ?? ''}`),
      put: (e, d, v) => void store.set(`${e}\n${d ?? ''}`, v),
    }
  }
  afterEach(() => vi.unstubAllGlobals())

  it('uses the public Claude source for cluster sessions while preserving its fallback', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ source: 'builtin', models: CLAUDE }) })
    vi.stubGlobal('fetch', fetchMock)
    const target = modelCatalogTarget('cluster', 'synthetic-pod')
    const { result } = renderHook(() => useEngineModels('claude', target.daemonId, { runtime: target.runtime }))
    expect(result.current.models).toEqual(CLAUDE)
    expect(result.current.loading).toBe(false)
    await waitFor(() => expect(result.current.source).toBe('builtin'))
    expect(fetchMock).toHaveBeenCalledWith('/api/models/claude?runtime=cluster', expect.anything())
    expect(result.current.models).toEqual(CLAUDE)
  })

  it('never substitutes a workstation inventory for a cluster engine without a source', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => CACHED })
    vi.stubGlobal('fetch', fetchMock)
    const target = modelCatalogTarget('cluster', 'synthetic-pod')
    const { result } = renderHook(() => useEngineModels('hermes', target.daemonId, { runtime: target.runtime }))
    await waitFor(() => expect(result.current.source).toBe('daemon'))
    expect(fetchMock).toHaveBeenCalledWith('/api/models/hermes?runtime=cluster', expect.anything())
    expect(result.current.models).toEqual([])
    expect(result.current.loading).toBe(false)
  })

  it('does not cache an old account’s late model response under the next account', async () => {
    const cache = memo()
    let account = 'account-a'
    const scoped = { ...cache, identity: () => account }
    let answer!: (v: unknown) => void
    vi.stubGlobal('fetch', vi.fn(() => new Promise(r => { answer = r })))
    const { result } = renderHook(() => useEngineModels('codex', undefined, { cache: scoped, runtime: 'cluster' }))
    account = 'account-b'
    await act(async () => answer({ ok: true, json: async () => ({ ...CACHED, source: 'live' }) }))
    expect(cache.store.size).toBe(0)
    expect(result.current.models).toEqual([])
  })

  it('surfaces cluster authentication failure instead of silently losing the picker', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 502, json: async () => ({ error: 'Reconnect Codex in Settings and retry' }) })))
    const { result } = renderHook(() => useEngineModels('codex', undefined, { runtime: 'cluster' }))
    await waitFor(() => expect(result.current.error).toBe('Reconnect Codex in Settings and retry'))
    expect(result.current.models).toEqual([])
  })

  it('bounds a stalled workstation response body without losing Claude fallback', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: () => new Promise(() => {}) }))
    const { result } = renderHook(() => useEngineModels('claude', 'workstation'))
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000) })
    expect(result.current.loading).toBe(false)
    expect(result.current.fallback).toBe(true)
    expect(result.current.models).toEqual(CLAUDE)
    expect(result.current.error).toContain('timed out')
    expect(result.current.error).not.toContain('Codex')
  })

  it('bounds a stalled cluster request and lets Retry fetch the catalog again', async () => {
    vi.useFakeTimers()
    let lateAnswer!: (value: unknown) => void
    const fetchMock = vi.fn()
      .mockImplementationOnce(() => new Promise(resolve => { lateAnswer = resolve }))
      .mockResolvedValue({ ok: true, json: async () => ({ ...CACHED, source: 'live' }) })
    vi.stubGlobal('fetch', fetchMock)
    const { result } = renderHook(() => useEngineModels('codex', undefined, { runtime: 'cluster' }))
    expect(result.current.loading).toBe(false)
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000) })
    expect(result.current.loading).toBe(false)
    expect(result.current.error).toContain('timed out')
    expect(fetchMock.mock.calls[0][1].signal.aborted).toBe(true)
    // Even a transport that ignores abort cannot replace the timeout with a late answer.
    await act(async () => lateAnswer({ ok: true, json: async () => ({ ...CACHED, source: 'live' }) }))
    expect(result.current.models).toEqual([])
    await act(async () => result.current.retry?.())
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(fetchMock.mock.calls[1][0]).toBe('/api/models/codex?runtime=cluster&refresh=1')
    expect(result.current.loading).toBe(false)
    expect(result.current.error).toBeUndefined()
    expect(result.current.models[0].id).toBe('gpt-cached')
  })

  it('keeps cluster controls available while refreshing and stops polling when disabled', async () => {
    vi.useFakeTimers()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ models: [], source: 'none', refreshing: true }) })
    vi.stubGlobal('fetch', fetchMock)
    const { result, rerender } = renderHook(({ enabled }) => useEngineModels('codex', undefined, { runtime: 'cluster', enabled }), { initialProps: { enabled: true } })
    expect(result.current.loading).toBe(false)
    await act(async () => {})
    expect(result.current.refreshing).toBe(true)
    await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
    expect(fetchMock).toHaveBeenCalledTimes(2)
    rerender({ enabled: false })
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000) })
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('replaces the cold cluster snapshot with models when background refresh completes', async () => {
    vi.useFakeTimers()
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({ ok: true, json: async () => ({ models: [], source: 'none', refreshing: true }) })
      .mockResolvedValue({ ok: true, json: async () => ({ ...CACHED, source: 'live' }) })
    vi.stubGlobal('fetch', fetchMock)
    const { result } = renderHook(() => useEngineModels('codex', undefined, { runtime: 'cluster' }))
    await act(async () => {})
    expect(result.current.models).toEqual([])
    expect(result.current.loading).toBe(false)
    await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
    expect(result.current.models[0].id).toBe('gpt-cached')
    expect(result.current.refreshing).toBe(false)
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000) })
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('keeps cached choices visible when a background refresh reports an error', async () => {
    const cache = memo()
    cache.put('codex', undefined, { ...CACHED, source: 'live' })
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => ({ models: [], source: 'none', error: 'Please reconnect Codex.', reconnect: true }) })))
    const { result } = renderHook(() => useEngineModels('codex', undefined, { runtime: 'cluster', cache }))
    await waitFor(() => expect(result.current.error).toBe('Please reconnect Codex.'))
    expect(result.current.reconnect).toBe(true)
    expect(result.current.models[0].id).toBe('gpt-cached')
    expect(result.current.loading).toBe(false)
  })

  it('requests the cluster runtime explicitly and keeps its per-model efforts', async () => {
    const fetchMock = vi.fn(async () => ({ ok: true, json: async () => ({
      source: 'live', models: [{ ...CACHED.models[0], efforts: ['low', 'high'], default_effort: 'low' }],
    }) }))
    vi.stubGlobal('fetch', fetchMock)
    const { result } = renderHook(() => useEngineModels('codex', undefined, { runtime: 'cluster' }))
    await waitFor(() => expect(result.current.models).toHaveLength(1))
    expect(fetchMock).toHaveBeenCalledWith('/api/models/codex?runtime=cluster', expect.anything())
    expect(result.current.models[0].efforts).toEqual(['low', 'high'])
    expect(result.current.models[0].default_effort).toBe('low')
  })

  it('shows the cached list at once, not loading, then the answer, and keeps it', async () => {
    const cache = memo()
    cache.put('codex', 'd1', CACHED)
    let answer!: (v: unknown) => void
    vi.stubGlobal('fetch', vi.fn(() => new Promise(r => { answer = r })))
    const { result } = renderHook(() => useEngineModels('codex', 'd1', { cache }))
    expect(result.current.loading).toBe(false)
    expect(result.current.models.map(m => m.id)).toEqual(['gpt-cached'])
    answer({ ok: true, json: () => Promise.resolve({ source: 'daemon', models: [{ id: 'gpt-new', name: 'GPT new', section: 'main' }] }) })
    await waitFor(() => expect(result.current.models.map(m => m.id)).toEqual(['gpt-new']))
    expect(cache.get('codex', 'd1')?.models.map(m => m.id)).toEqual(['gpt-new'])
  })

  it('keeps the cached list when the refresh fails, rather than the built-in fallback', async () => {
    const cache = memo()
    cache.put('codex', 'd1', CACHED)
    let fail!: () => void
    const fetchMock = vi.fn(() => new Promise((_, reject) => { fail = () => reject(new Error('down')) }))
    vi.stubGlobal('fetch', fetchMock)
    const { result } = renderHook(() => useEngineModels('codex', 'd1', { cache }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalled())
    await act(async () => { fail() })
    expect(result.current.fallback).toBe(false)
    expect(result.current.loading).toBe(false)
    expect(result.current.models.map(m => m.id)).toEqual(['gpt-cached'])
  })
})
