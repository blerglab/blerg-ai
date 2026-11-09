import { act, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { BlackboardButton } from './BlackboardTabs'
import { invalidateBlackboardReads, resetBlackboardReadsForTest } from '../lib/blackboardReads'
import { useSessionTabs } from '../hooks/useSessionTabs'

vi.mock('../ws', () => import('../test/wsMock'))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

function Harness({ sessionId }: { sessionId: string }) {
  const tabs = useSessionTabs('', sessionId, false)
  return <BlackboardButton sessionId={sessionId} tabs={tabs} />
}

const reply = (ok: boolean, blackboards: unknown[] = []) => Promise.resolve({ ok, json: async () => ({ blackboards }) })

beforeEach(() => { resetBlackboardReadsForTest(); apiFetch.mockReset() })
afterEach(() => { vi.restoreAllMocks() })

// BLG-431: this fetch used to have no cache at all - every mount re-read /api/blackboards, even
// switching back to the same session seconds later. Proves the fix: one read is shared across
// remounts of the same session within the freshness window, a different session still reads
// fresh, and the cached list is on screen at once (no flash to hidden) on a cached remount.
it('reuses a recent read across remounts of the same session, reads fresh for a different one', async () => {
  const list = [{ id: 'bb-1', name: 'Project', milestones: [] }]
  apiFetch.mockImplementation(() => reply(true, list))
  const a = render(<MemoryRouter><Harness sessionId="sess-a" /></MemoryRouter>)
  await waitFor(() => expect(a.getByRole('button', { name: 'Open blackboard' })).toBeTruthy())
  expect(apiFetch).toHaveBeenCalledTimes(1)
  a.unmount()

  // A cached remount of the same session: the button is there at once, no second read.
  const again = render(<MemoryRouter><Harness sessionId="sess-a" /></MemoryRouter>)
  expect(again.getByRole('button', { name: 'Open blackboard' })).toBeTruthy()
  expect(apiFetch).toHaveBeenCalledTimes(1)
  again.unmount()

  // A different session is not served from sess-a's cache entry.
  const other = render(<MemoryRouter><Harness sessionId="sess-b" /></MemoryRouter>)
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2))
  expect(other.queryByRole('button', { name: 'Open blackboard' })).toBeTruthy()
})

it('reads fresh again after an identity change clears the cache', async () => {
  apiFetch.mockImplementation(() => reply(true, [{ id: 'bb-1', name: 'Project', milestones: [] }]))
  const view = render(<MemoryRouter><Harness sessionId="sess-a" /></MemoryRouter>)
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
  view.unmount()
  act(() => { window.dispatchEvent(new Event('blerg-identity-changed')) })
  render(<MemoryRouter><Harness sessionId="sess-a" /></MemoryRouter>)
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2))
})

it('always shows the button, and reads fresh after a blackboard is created', async () => {
  apiFetch.mockImplementation(() => reply(true, []))
  const view = render(<MemoryRouter><Harness sessionId="sess-c" /></MemoryRouter>)
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
  expect(view.getByRole('button', { name: 'Open blackboard' })).toBeTruthy()
  view.unmount()
  invalidateBlackboardReads()
  render(<MemoryRouter><Harness sessionId="sess-c" /></MemoryRouter>)
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2))
})
