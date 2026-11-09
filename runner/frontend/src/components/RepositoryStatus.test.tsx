import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import RepositoryStatus from './RepositoryStatus'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../apiFetch', () => ({ apiFetch: (...args: Parameters<typeof fetch>) => fetch(...args) }))
beforeEach(() => vi.useFakeTimers())
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })
const good = { result: { ok: true, status: { branch: 'main', changed: 2, untracked: 1, partial: true } } }
const flush = async (ms = 400) => { await act(async () => { await vi.advanceTimersByTimeAsync(ms) }) }
it('keeps partial state through prolonged contention, quietly retries, and recovers', async () => {
  const fetcher = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify(good)))
  render(<RepositoryStatus sessionId="s" path="." name="app" active />)
  await flush()
  expect(screen.getByRole('img', { name: '3+ uncommitted' })).toHaveAttribute('title', expect.stringContaining('incomplete'))
  fetcher.mockImplementation(async () => new Response('{"code":"busy"}', { status: 409 }))
  fireEvent(window, new Event('focus'))
  await flush(15000)
  expect(screen.getByRole('img', { name: '3+ uncommitted' })).toBeVisible()
  expect(screen.queryByRole('button')).toBeNull()
  fetcher.mockImplementation(async () => new Response(JSON.stringify({ result: { ok: true, status: { ...good.result.status, changed: 0, untracked: 0, partial: false } } })))
  await flush(2500)
  expect(screen.getByRole('img', { name: 'Clean' })).toBeVisible()
})
it('shows real failures with a reason and quiet refresh, then recovers', async () => {
  const fetcher = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => new Response('{"result":{"ok":false,"status_reason":"git_failed"}}'))
  render(<RepositoryStatus sessionId="s" path="." name="app" active />)
  await flush()
  expect(screen.getByRole('img', { name: 'Git status unavailable' })).toHaveAttribute('title', expect.stringContaining('Git status command'))
  fetcher.mockImplementation(async () => new Response(JSON.stringify(good)))
  fireEvent.click(screen.getByRole('button', { name: 'Retry state for app' }))
  await flush()
  expect(screen.getByRole('img', { name: '3+ uncommitted' })).toBeVisible()
})
it('cancels busy retries when hidden and does not reuse another repository state', async () => {
  const fetcher = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => new Response('{"result":{"ok":false,"error_code":"busy"}}'))
  const view = render(<RepositoryStatus sessionId="s" path="." name="app" active />)
  await flush()
  expect(screen.getByRole('img', { name: 'Reading state…' })).toBeVisible()
  view.rerender(<RepositoryStatus sessionId="s" path="." name="app" active={false} />)
  await flush(15000)
  expect(fetcher).toHaveBeenCalledTimes(1)
  view.unmount()
})
it('aborts an in-flight read on navigation and hides the previous repository state', async () => {
  const fetcher = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => new Response(JSON.stringify(good)))
  const view = render(<RepositoryStatus sessionId="s" path="app" name="app" active />)
  await flush()
  expect(screen.getByRole('img', { name: '3+ uncommitted' })).toBeVisible()
  let signal: AbortSignal | null | undefined
  fetcher.mockImplementation((_url, init) => { signal = init?.signal; return new Promise<Response>(() => {}) })
  view.rerender(<RepositoryStatus sessionId="s" path="library" name="library" active />)
  expect(screen.getByRole('img', { name: 'Reading state…' })).toBeVisible()
  await flush()
  view.unmount()
  expect(signal?.aborted).toBe(true)
})

it('keeps clean and loading state quiet in the graph summary, but shows changes', async () => {
  const fetcher = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => new Response(JSON.stringify({ result: { ok: true, status: { branch: 'main', changed: 0, untracked: 0, partial: false } } })))
  render(<RepositoryStatus sessionId="quiet" path="." name="app" active quiet />)
  expect(screen.queryByRole('img')).toBeNull()
  await flush()
  expect(screen.queryByRole('img')).toBeNull()
  fetcher.mockImplementation(async () => new Response(JSON.stringify(good)))
  fireEvent(window, new Event('focus'))
  await flush()
  expect(screen.getByRole('img', { name: '3+ uncommitted' })).toBeVisible()
})
