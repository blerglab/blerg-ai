import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import WorkspaceTree from './WorkspaceTree'
import { inputClock } from '../../../../packages/chat/src/model/clock'

const read = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch: read }))
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../lib/chatTransport', () => ({ blergTransport: () => ({ files: null }) }))
beforeEach(() => { read.mockReset() })
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })

it.each(['host', 'sandbox', 'cluster'])('lists agent-written scratch files without repository members on %s', async runtime => {
  read.mockResolvedValue(new Response(JSON.stringify({ result: { ok: true, launch_generation: 'scratch', generation: 1, entries: [{ name: 'story.md', path: 'story.md', directory: false }], truncated: false }, members: [], runtime })))
  render(<WorkspaceTree sessionId={`scratch-${runtime}`} active />)
  expect(await screen.findByRole('treeitem', { name: 'story.md' })).toBeInTheDocument()
  expect(read).toHaveBeenCalledWith(expect.stringContaining('op=list&path='), expect.anything())
})

it('keeps the unavailable cause visible during automatic recovery instead of restarting the skeleton', async () => {
  read.mockImplementation(async () => new Response(JSON.stringify({ error: 'This workspace started without file browsing.', code: 'unavailable' }), { status: 409 }))
  vi.useFakeTimers()
  render(<WorkspaceTree sessionId="scratch-unavailable" active />)
  await act(async () => {})
  await act(async () => { await vi.advanceTimersByTimeAsync(9000) })
  expect(screen.getByRole('status')).toHaveTextContent('This workspace started without file browsing.')
  read.mockImplementation(() => new Promise(() => {}))
  await act(async () => { await vi.advanceTimersByTimeAsync(5500) })
  expect(screen.getByRole('status')).toHaveTextContent('This workspace started without file browsing.')
  expect(screen.queryByLabelText('workspace')).toBeNull()
})

it('leaves the skeleton with a cause when listing never replies', async () => {
  vi.useFakeTimers()
  read.mockImplementation(() => new Promise(() => {}))
  render(<WorkspaceTree sessionId="scratch-stalled" active />)
  await act(async () => { await vi.advanceTimersByTimeAsync(20_000) })
  expect(screen.queryByLabelText('workspace')).toBeNull()
  expect(screen.getByRole('status')).toHaveTextContent('The workspace read timed out. Try Refresh.')
  expect(screen.getByRole('button', { name: 'Refresh workspace' })).not.toHaveAttribute('aria-disabled', 'true')
  expect(read.mock.calls[0][1].signal.aborted).toBe(true)
})

it('logs a typed, rate-limited failure without private transport details', async () => {
  const log = vi.spyOn(console, 'warn').mockImplementation(() => {})
  const clock = vi.spyOn(inputClock, 'now').mockReturnValue(0)
  read.mockImplementation(async () => new Response(JSON.stringify({ error: 'Workspace listing is unavailable.', code: 'PRIVATE_CANARY workspace path and credential' }), { status: 500 }))
  render(<WorkspaceTree sessionId="scratch-log" active />)
  await screen.findByText('Workspace listing is unavailable.')
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Refresh workspace' })) })
  await screen.findByText('Workspace listing is unavailable.')
  expect(log).toHaveBeenCalledTimes(1)
  expect(log).toHaveBeenCalledWith('Workspace listing failed', { cause: 'read_failed', operation: 'list', suppressed: 0 })
  clock.mockReturnValue(61_000)
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Refresh workspace' })) })
  await screen.findByText('Workspace listing is unavailable.')
  expect(log).toHaveBeenLastCalledWith('Workspace listing failed', { cause: 'read_failed', operation: 'list', suppressed: 1 })
  expect(JSON.stringify(log.mock.calls)).not.toContain('PRIVATE_CANARY')
})

it('keeps the skeleton absent after its delay during a recovery read', async () => {
  vi.useFakeTimers()
  read.mockImplementation(async () => new Response(JSON.stringify({ error: 'The workspace is reconnecting.', code: 'unavailable' }), { status: 409 }))
  render(<WorkspaceTree sessionId="scratch-delayed-skeleton" active />)
  await act(async () => {})
  await act(async () => { await vi.advanceTimersByTimeAsync(9000) })
  read.mockImplementation(() => new Promise(() => {}))
  // Commit the recovery render first, so DelayedSkeleton can install its own timer.
  await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
  await act(async () => { await vi.advanceTimersByTimeAsync(151) })
  expect(screen.queryByLabelText('workspace')).toBeNull()
  expect(screen.getByRole('status')).toHaveTextContent('The workspace is reconnecting.')
})

it('retains the last daemon timeout cause when the overall deadline cancels slow retries', async () => {
  vi.useFakeTimers()
  const log = vi.spyOn(console, 'warn').mockImplementation(() => {})
  read.mockImplementation(() => new Promise(resolve => setTimeout(() => resolve(new Response(JSON.stringify({ code: 'daemon_timeout', error: 'The workspace runtime did not reply' }), { status: 409 })), 3000)))
  render(<WorkspaceTree sessionId="scratch-slow-refusals" active />)
  await act(async () => {})
  await act(async () => { await vi.advanceTimersByTimeAsync(15_000) })
  expect(screen.getByRole('status')).toHaveTextContent('The workspace runtime did not reply')
  expect(screen.queryByLabelText('workspace')).toBeNull()
  expect(log).toHaveBeenCalledWith('Workspace listing failed', { cause: 'daemon_timeout', operation: 'list', suppressed: 0 })
})

it.each([false, true])('stops unsupported recovery (after transient refusal: %s), but allows Refresh', async transient => {
  vi.useFakeTimers()
  let code = transient ? 'unavailable' : 'unsupported'
  read.mockImplementation(async () => new Response(JSON.stringify({ code, error: code === 'unsupported' ? 'File browsing is unavailable for this runtime.' : 'The workspace is reconnecting.' }), { status: 409 }))
  render(<WorkspaceTree sessionId={`scratch-unsupported-${transient}`} active />)
  await act(async () => {})
  if (transient) {
    await act(async () => { await vi.advanceTimersByTimeAsync(9000) })
    code = 'unsupported'
    await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
  }
  expect(screen.getByRole('status')).toHaveTextContent('File browsing is unavailable for this runtime.')
  const attempts = read.mock.calls.length
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000) })
  await act(async () => { fireEvent(document, new Event('visibilitychange')) })
  expect(read).toHaveBeenCalledTimes(attempts)
  expect(screen.getByRole('button', { name: 'Refresh workspace' })).not.toHaveAttribute('aria-disabled', 'true')
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Refresh workspace' })) })
  expect(read).toHaveBeenCalledTimes(attempts + 1)
})
