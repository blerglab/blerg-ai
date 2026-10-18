import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useSessionBoards } from './sessionBoards'

vi.mock('../apiFetch', () => ({ apiFetch: (url: string, options: RequestInit) => fetch(url, options) }))

afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })
describe('session board discovery', () => {
  it('times out a hung response, ignores a late answer, and retries', async () => {
    vi.useFakeTimers()
    let resolve!: (r: unknown) => void
    const pending = new Promise(r => { resolve = r })
    const fetcher = vi.fn().mockReturnValueOnce(pending).mockResolvedValue({ ok: true, json: async () => ({ configured: true, boards: [{ id: 'b', name: 'Work' }] }) })
    vi.stubGlobal('fetch', fetcher)
    const { result } = renderHook(() => useSessionBoards(true))
    await act(async () => { await vi.advanceTimersByTimeAsync(8000) })
    expect(result.current.state).toBe('failed')
    expect(fetcher.mock.calls[0][1].signal.aborted).toBe(true)
    await act(async () => { resolve({ ok: true, json: async () => ({ configured: false, boards: [] }) }) })
    expect(result.current.state).toBe('failed')
    await act(async () => result.current.retry())
    expect(result.current.state).toBe('ready')
    expect(result.current.boards).toEqual([{ id: 'b', name: 'Work' }])
  })
  it('does not discover for a closed sheet', () => {
    const fetcher = vi.fn()
    vi.stubGlobal('fetch', fetcher)
    renderHook(() => useSessionBoards(false))
    expect(fetcher).not.toHaveBeenCalled()
  })
  it('refuses malformed replies instead of offering unverified boards', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ configured: true, boards: [{ name: 'Missing ID' }] }) }))
    const { result } = renderHook(() => useSessionBoards(true))
    await waitFor(() => expect(result.current.state).toBe('failed'))
    expect(result.current.boards).toEqual([])
  })
})
