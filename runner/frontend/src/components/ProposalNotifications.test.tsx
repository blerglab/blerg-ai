import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { BrandHeader } from '../App'
import { ProposalBell } from './ProposalNotifications'
import { resetProposalWarnings } from '../lib/proposalAttention'
import { useToastStore } from '../hooks/useToastStore'
import { useMessageStore } from '../hooks/useMessageStore'
import type { ProposalInfo } from '../types'

const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
vi.mock('../ws', () => import('../test/wsMock'))
const proposal = (id: string, title: string, expires: number): ProposalInfo => ({ id, state: 'pending', kind: 'board_file', connection_id: '', connection_name: 'Board', tool: 'file_ticket', arguments: { title, type: 'bug', body: '## Details\n\nFull card body', repos: ['example-org/example-repo'] }, arguments_raw: '{}', agent_summary: '', session_id: 'sess-a', cron_id: null, created_at: new Date().toISOString(), expires_at: new Date(expires).toISOString(), decided_at: null, decided_by: null, result: null })
let items: ProposalInfo[] = []
beforeEach(() => {
  useToastStore.setState({ toasts: [] })
  resetProposalWarnings()
  apiFetch.mockReset()
  apiFetch.mockImplementation(async () => ({ ok: true, status: 200, json: async () => ({ proposals: items }) }))
})
afterEach(() => vi.useRealTimers())
async function mount() {
  render(<MemoryRouter><BrandHeader /></MemoryRouter>)
  await act(async () => { for (let i = 0; i < 12; i++) await Promise.resolve() })
}
describe('questions in the bell', () => {
  afterEach(() => useMessageStore.setState({ messages: [] }))
  it('lists an open question the agent did not wait on that this person answers, and only that', async () => {
    items = []
    const at = new Date().toISOString()
    useMessageStore.setState({ messages: [
      { id: 'q1', session_id: 'sess-q', kind: 'ask', body: 'Ship the blue one?', status: 'open', created_at: at, no_wait: true, answerable: true },
      { id: 'q3', session_id: 'sess-shared', kind: 'ask', body: 'Someone else\'s question', status: 'open', created_at: at, no_wait: true },
      { id: 'q2', session_id: 'sess-q', kind: 'ask', body: 'Answered already?', status: 'answered', answer: 'yes', created_at: at, no_wait: true },
      { id: 'a1', session_id: 'sess-q', kind: 'ask', body: 'A blocking ask', status: 'open', created_at: at },
    ] })
    await mount()
    const bell = screen.getByRole('button', { name: /Notifications/ })
    expect(bell.getAttribute('aria-label')).toContain('1 question')
    expect(bell.textContent).toContain('1')
    fireEvent.click(bell)
    const rows = screen.getAllByTestId('bell-question')
    expect(rows).toHaveLength(1)
    expect(rows[0].textContent).toContain('Ship the blue one?')
    expect(screen.getByRole('link', { name: 'Answer in the session' }).getAttribute('href')).toBe('/sessions/sess-q')
  })
})

describe('proposal notifications', () => {
  it('decides "past its deadline" by the server\'s clock', async () => {
    items = [proposal('p1', 'Skewed card', Date.now() - 60_000)]
    // The browser runs ten minutes fast: the server says nine minutes are left.
    apiFetch.mockImplementation(async () => ({ ok: true, status: 200, json: async () => ({ proposals: items, server_time: new Date(Date.now() - 10 * 60_000).toISOString() }) }))
    await mount()
    expect(screen.getByRole('button', { name: /Notifications/ }).getAttribute('aria-label')).toContain('(1 pending')
  })

  it('does not count a proposal past its deadline, which it still lists', async () => {
    items = [proposal('p1', 'Waiting card', Date.now() + 7 * 86400_000), proposal('p2', 'Overdue card', Date.now() - 60_000)]
    await mount()
    const bell = screen.getByRole('button', { name: /Notifications/ })
    expect(bell.getAttribute('aria-label')).toContain('(1 pending')
    expect(bell.getAttribute('title')).toContain('1 proposals')
    expect(bell.textContent).toContain('1')
    expect(bell.textContent).not.toContain('2')
    fireEvent.click(bell)
    expect(screen.getByText('Waiting card')).toBeTruthy()
    expect(screen.getByText('Overdue card')).toBeTruthy()
  })
  it('keeps both successive proposals in the bell without browser notification permission', async () => {
    items = [proposal('p1', 'First card', Date.now() + 7 * 86400_000)]
    await mount()
    fireEvent.click(screen.getByRole('button', { name: /Notifications/ }))
    expect(screen.getByText('First card')).toBeTruthy()
    items = [...items, proposal('p2', 'Second card', Date.now() + 7 * 86400_000)]
    await act(async () => { fireEvent(window, new Event('focus')); for (let i = 0; i < 12; i++) await Promise.resolve() })
    expect(screen.getByText('Second card')).toBeTruthy()
    expect(screen.getAllByRole('link', { name: 'Open session' })).toHaveLength(2)
  })
  it('fires one notification when the deadline enters its warning window', async () => {
    vi.useFakeTimers({ toFake: ['Date', 'setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
    items = [proposal('p1', 'Expiring card', Date.now() + 86400_000 + 2_000)]
    await mount()
    expect(useToastStore.getState().toasts).toHaveLength(0)
    await act(async () => { vi.advanceTimersByTime(3_000) })
    expect(useToastStore.getState().toasts).toHaveLength(1)
    expect(useToastStore.getState().toasts[0].title).toBe('Proposal expires soon')
    await act(async () => { vi.advanceTimersByTime(3_000) })
    expect(useToastStore.getState().toasts).toHaveLength(1)
    fireEvent.click(screen.getByRole('button', { name: /Notifications/ }))
    expect(screen.getByText(/Expires soon/)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Extend 7 days' })).toBeTruthy()
  })
  it('extends the reviewed deadline and refreshes both the bell and warning', async () => {
    items = [proposal('p1', 'Extend this card', Date.now() + 3600_000)]
    const expected = items[0].expires_at
    apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.endsWith('/extend')) {
        expect(init?.method).toBe('POST')
        expect(JSON.parse(String(init?.body))).toEqual({ expected_expires_at: expected })
        items = [{ ...items[0], expires_at: new Date(Date.parse(expected) + 7 * 86400_000).toISOString() }]
        return { ok: true, status: 200, json: async () => items[0] }
      }
      return { ok: true, status: 200, json: async () => ({ proposals: items }) }
    })
    await mount()
    fireEvent.click(screen.getByRole('button', { name: /Notifications/ }))
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Extend 7 days' })); for (let i = 0; i < 12; i++) await Promise.resolve() })
    expect(apiFetch.mock.calls.filter(([url]) => String(url).endsWith('/extend'))).toHaveLength(1)
    expect(screen.queryByRole('button', { name: 'Extend 7 days' })).toBeNull()
    expect(screen.queryByText(/Expires soon/)).toBeNull()
    expect(screen.getByText('Extend this card')).toBeTruthy()
  })
  it('shows the tool and connection first and labels a mismatched title as the agent’s', async () => {
    items = [{ ...proposal('p1', 'Fix a typo', Date.now() + 7 * 86400_000), kind: 'mcp', tool: 'delete_branch', connection_name: 'Source host', arguments: { title: 'Fix a typo', branch: 'main' } }]
    await mount()
    fireEvent.click(screen.getByRole('button', { name: /Notifications/ }))
    const action = screen.getByTestId('proposal-action-p1')
    expect(action.textContent).toBe('Runs delete_branch on Source host')
    const title = screen.getByTestId('proposal-agent-title-p1')
    expect(title.textContent).toBe('Title in the agent’s request: Fix a typo')
    expect(action.compareDocumentPosition(title) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
  it('warns once per deadline even when a session page remounts its bell', async () => {
    items = [proposal('p1', 'Expiring card', Date.now() + 3600_000)]
    const inbox = { pending: items, refresh: async () => {}, error: null }
    const first = render(<MemoryRouter><ProposalBell inbox={inbox} /></MemoryRouter>)
    await act(async () => { await Promise.resolve() })
    expect(useToastStore.getState().toasts).toHaveLength(1)
    first.unmount()
    render(<MemoryRouter><ProposalBell inbox={inbox} /></MemoryRouter>)
    await act(async () => { await Promise.resolve() })
    expect(useToastStore.getState().toasts).toHaveLength(1)
  })
  it('offers no extension past the 30-day limit and says why', async () => {
    items = [{ ...proposal('p1', 'Old card', Date.now() + 3600_000), extendable: false, max_expires_at: new Date(Date.now() + 3600_000).toISOString() }]
    await mount()
    fireEvent.click(screen.getByRole('button', { name: /Notifications/ }))
    expect((screen.getByRole('button', { name: 'Extend 7 days' }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByText('Proposals can stay open up to 30 days.')).toBeTruthy()
  })
  it('names a last, shorter extension', async () => {
    items = [{ ...proposal('p1', 'Nearly old card', Date.now() + 3600_000), extendable: true, max_expires_at: new Date(Date.now() + 3 * 86400_000).toISOString() }]
    await mount()
    fireEvent.click(screen.getByRole('button', { name: /Notifications/ }))
    expect((screen.getByRole('button', { name: 'Extend to the 30-day limit' }) as HTMLButtonElement).disabled).toBe(false)
  })
})
