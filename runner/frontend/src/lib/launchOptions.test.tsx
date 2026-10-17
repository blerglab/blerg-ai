import { act, renderHook, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useLaunchOptions } from './launchOptions'

const fetcher = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...args: unknown[]) => fetcher(...args) }))
const descriptor = (id: string) => ({ id, name: id.toUpperCase(), runtimes: ['daemon'], terminal: true, restricted: true, plugins: false, credential_import: false, models: { models: [], source: 'none' } })
const reply = (ids: string[], state = 'known') => ({ runtime: 'daemon', state, engines: ids.map(descriptor) })
const ok = (value: unknown) => Promise.resolve({ ok: true, json: async () => value })
beforeEach(() => { fetcher.mockReset() })

describe('launch-context discovery', () => {
  it('discovers a future engine without a frontend inventory change', async () => {
    fetcher.mockImplementation(() => ok(reply(['future-engine'])))
    const { result } = renderHook(() => useLaunchOptions({ runtime: 'daemon', daemonId: 'd1', kind: 'agent' }))
    await waitFor(() => expect(result.current.state).toBe('known'))
    expect(result.current.engines.map(e => e.id)).toEqual(['future-engine'])
    expect(String(fetcher.mock.calls[0][0])).toContain('daemon_id=d1')
  })

  it('separates account-only discovery when credential policy changes', async () => {
    fetcher.mockImplementation(() => ok(reply(['codex'])))
    const { result, rerender } = renderHook(({ personalOnly }) => useLaunchOptions({ runtime: 'cluster', restricted: true, personalOnly }), { initialProps: { personalOnly: false } })
    await waitFor(() => expect(result.current.state).toBe('known'))
    expect(String(fetcher.mock.calls[0][0])).not.toContain('personal_only')
    rerender({ personalOnly: true })
    expect(result.current.engines).toEqual([])
    await waitFor(() => expect(result.current.state).toBe('known'))
    expect(String(fetcher.mock.calls.at(-1)![0])).toContain('personal_only=1')
  })

  it('distinguishes known empty from unavailable discovery and supports retry', async () => {
    fetcher.mockImplementationOnce(() => ok(reply([]))).mockImplementationOnce(() => ok({ ...reply([], 'unknown'), error: 'Credential service unavailable' }))
    const { result } = renderHook(() => useLaunchOptions({ runtime: 'cluster', kind: 'agent' }))
    await waitFor(() => expect(result.current.state).toBe('known'))
    expect(result.current.engines).toEqual([])
    act(() => result.current.retry())
    await waitFor(() => expect(result.current.state).toBe('unknown'))
    expect(result.current.error).toBe('Credential service unavailable')
    expect(String(fetcher.mock.calls[1][0])).toContain('refresh=1')
  })

  it('hides the previous target immediately and ignores its late response', async () => {
    let finish: (value: unknown) => void = () => {}
    fetcher.mockImplementationOnce(() => new Promise(resolve => { finish = resolve })).mockImplementation(() => ok(reply(['codex'])))
    const { result, rerender } = renderHook(({ daemonId }) => useLaunchOptions({ runtime: 'daemon', daemonId, kind: 'agent' }), { initialProps: { daemonId: 'd1' } })
    rerender({ daemonId: 'd2' })
    expect(result.current.engines).toEqual([])
    await waitFor(() => expect(result.current.engines[0]?.id).toBe('codex'))
    await act(async () => finish({ ok: true, json: async () => reply(['claude']) }))
    expect(result.current.engines.map(e => e.id)).toEqual(['codex'])
  })

  it('bounds a request even if fetch does not honor cancellation', async () => {
    vi.useFakeTimers()
    try {
      fetcher.mockImplementation(() => new Promise(() => {}))
      const { result } = renderHook(() => useLaunchOptions({ runtime: 'cluster', kind: 'agent' }))
      await act(async () => { vi.advanceTimersByTime(10_000) })
      expect(result.current.state).toBe('unknown')
      expect(result.current.error).toMatch(/timed out/i)
    } finally { vi.useRealTimers() }
  })

  it.each([undefined, null, 42, ['codex']])('rejects non-string descriptor IDs (%j) without coercing an engine', async id => {
    fetcher.mockImplementation(() => ok({ state: 'known', engines: [{ id, name: 'Missing ID' }] }))
    const { result } = renderHook(() => useLaunchOptions({ runtime: 'cluster', kind: 'agent' }))
    await waitFor(() => expect(result.current.state).toBe('unknown'))
    expect(result.current.engines).toEqual([])
  })

  it('does not infer a static engine when the response is malformed', async () => {
    fetcher.mockImplementation(() => ok({ engines: [{ id: '../bad', name: 'Bad' }] }))
    const { result } = renderHook(() => useLaunchOptions({ runtime: 'cluster', kind: 'agent' }))
    await waitFor(() => expect(result.current.state).toBe('unknown'))
    expect(result.current.engines).toEqual([])
  })
})

describe('background launch discovery', () => {
  it('polls a cold snapshot until readiness is known', async () => {
    fetcher.mockImplementationOnce(() => ok({ ...reply([], 'unknown'), refreshing: true })).mockImplementation(() => ok(reply(['codex'])))
    const { result } = renderHook(() => useLaunchOptions({ runtime: 'cluster', kind: 'agent' }))
    expect(result.current.engines).toEqual([])
    await waitFor(() => expect(result.current.state).toBe('known'))
    expect(result.current.engines.map(e => e.id)).toEqual(['codex'])
    expect(fetcher).toHaveBeenCalledTimes(2)
  })

  it('stops an endlessly refreshing readiness snapshot after the bounded cycle', async () => {
    vi.useFakeTimers()
    try {
      fetcher.mockImplementation(() => ok({ ...reply([], 'unknown'), refreshing: true }))
      const { result } = renderHook(() => useLaunchOptions({ runtime: 'cluster', kind: 'agent' }))
      await act(async () => { await vi.advanceTimersByTimeAsync(8_000) })
      expect(result.current.state).toBe('unknown')
      expect(result.current.error).toMatch(/timed out/i)
      const count = fetcher.mock.calls.length
      await act(async () => { await vi.advanceTimersByTimeAsync(2_000) })
      expect(fetcher).toHaveBeenCalledTimes(count)
    } finally { vi.useRealTimers() }
  })
})
