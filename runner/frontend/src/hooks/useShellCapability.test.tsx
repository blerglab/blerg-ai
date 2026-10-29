import { act, fireEvent, render, renderHook, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { useShellCapability } from './useShellCapability'

const read = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch: read }))
afterEach(() => vi.resetAllMocks())

function Capability() {
  const available = useShellCapability('legacy', 'idle')
  return <div>{available ? 'Shell available' : 'Shell not ready'}</div>
}

it('rechecks shell availability after the resumed explorer attaches', async () => {
  read.mockResolvedValueOnce(new Response(JSON.stringify({ available: false })))
  render(<Capability />)
  await waitFor(() => expect(read).toHaveBeenCalledTimes(1))
  await screen.findByText('Shell not ready')
  read.mockResolvedValueOnce(new Response(JSON.stringify({ available: true })))
  fireEvent(window, new CustomEvent('blerg-session-workspace-ready', { detail: 'other-session' }))
  expect(read).toHaveBeenCalledTimes(1)
  fireEvent(window, new CustomEvent('blerg-session-workspace-ready', { detail: 'legacy' }))
  await screen.findByText('Shell available')
  expect(read).toHaveBeenCalledTimes(2)
})

it.each([false, true])('distinguishes loading from a resolved capability (%s)', async available => {
  let finish!: (response: Response) => void
  read.mockImplementationOnce(() => new Promise<Response>(resolve => { finish = resolve }))
  const { result } = renderHook(() => useShellCapability('legacy', 'idle'))
  expect(result.current).toBeNull()
  await act(async () => { finish(new Response(JSON.stringify({ available }))) })
  expect(result.current).toBe(available)
})
it('resolves an unavailable capability after a network failure', async () => {
  read.mockRejectedValueOnce(new Error('Offline'))
  const { result } = renderHook(() => useShellCapability('legacy', 'idle'))
  expect(result.current).toBeNull()
  await waitFor(() => expect(result.current).toBe(false))
})
// #267: a busy desktop is not a "no". The capability stays pending and is asked again.
it('stays pending and asks again while the desktop is busy', async () => {
  vi.useFakeTimers()
  try {
    const busy = () => new Response(JSON.stringify({ error: 'desktop busy', code: 'runtime_busy' }), { status: 503 })
    read.mockResolvedValueOnce(busy()).mockResolvedValueOnce(busy()).mockResolvedValueOnce(new Response(JSON.stringify({ available: true })))
    const { result } = renderHook(() => useShellCapability('legacy', 'idle'))
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(result.current).toBeNull()
    await act(async () => { await vi.advanceTimersByTimeAsync(1000) })
    expect(result.current).toBeNull()
    await act(async () => { await vi.advanceTimersByTimeAsync(1000) })
    expect(result.current).toBe(true)
    expect(read).toHaveBeenCalledTimes(3)
  } finally { vi.useRealTimers() }
})
it('gives up after a bounded number of busy answers', async () => {
  vi.useFakeTimers()
  try {
    read.mockImplementation(async () => new Response(JSON.stringify({ code: 'runtime_busy' }), { status: 503 }))
    const { result } = renderHook(() => useShellCapability('legacy', 'idle'))
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000) })
    expect(result.current).toBe(false)
    expect(read).toHaveBeenCalledTimes(6)
  } finally { vi.useRealTimers() }
})
