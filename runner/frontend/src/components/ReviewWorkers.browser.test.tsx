import { expect, it, vi } from 'vitest'
import { act, render, screen, within, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import SessionList from './SessionList'
import SessionDockWindows from './SessionDockWindows'
import SessionCard from './SessionCard'
import { useSessionStore } from '../hooks/useSessionStore'
import { useMessageStore } from '../hooks/useMessageStore'
import { makeSession, makeDaemon } from '../test/fixtures'
import '../index.css'
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn(async (url: string) => {
  const id = url.split('/')[3]
  return { ok: true, status: 200, json: async () => ({ session_id: id, proposals: id === 'worker-3' ? 1 : 0, review_requests: id === 'worker-4' ? [{}] : [], answer_requests: id === 'worker-5' ? [{}] : [], tool_changes: id === 'worker-6' ? 1 : 0 }) }
}) }))
it('counts all roles, keeps channel unread and all decision kinds visible through phone search at 390 px', async () => {
  await page.viewport(390, 844)
  localStorage.clear()
  const root = makeSession({ id: 'root', title: 'Search this workspace', status: 'idle' })
  const channels = [makeSession({ id: 'channel-0', title: 'Blocked discussion', parent_session_id: 'root', status: 'waiting', unread: true }), makeSession({ id: 'channel-1', parent_session_id: 'root', status: 'idle' })]
  const workers = Array.from({ length: 7 }, (_, i) => makeSession({ id: `worker-${i}`, title: `Worker ${i}`, parent_session_id: i === 6 ? 'worker-0' : 'root', started_by: { kind: 'session' }, status: i === 1 ? 'error' : 'idle', unread: true }))
  useMessageStore.setState({ messages: [{ id: 'ask', session_id: 'worker-2', kind: 'ask', status: 'open', answerable: true, body: 'Choose', created_at: '2026-10-31T12:00:00Z' }] })
  useSessionStore.setState({ sessions: [root, ...channels, ...workers], daemons: [makeDaemon()] })
  render(<MemoryRouter><SessionList /></MemoryRouter>)
  const card = document.querySelector<HTMLElement>('[data-session-branch="root"]')!
  expect(within(card).getByTestId('hidden-children')).toHaveAccessibleName('2 channels, 7 workers')
  expect(within(card).getByTestId('hidden-children')).toHaveTextContent('↳ 9')
  expect(within(card).getByTestId('unread-dot')).toBeVisible()
  await waitFor(() => expect(within(card).getByTestId('card-attention-count')).toHaveTextContent('1 failed, 6 need you'))
  await userEvent.fill(screen.getByPlaceholderText('Search workspaces…'), 'Search this workspace')
  expect(screen.queryByTestId('nested-sessions')).toBeNull()
  expect(within(card).getByTestId('card-attention-count')).toHaveTextContent('1 failed, 6 need you')
  expect(within(card).getByTestId('hidden-children')).toHaveAccessibleName('2 channels, 7 workers')
  const shots = import.meta.env.VITE_CHROME_SHOTS_DIR
  if (shots) await page.screenshot({ path: `${shots}/review-phone-attention-390.png` })
  act(() => useSessionStore.setState(s => ({ sessions: s.sessions.map(x => x.id === 'channel-0' ? { ...x, unread: false } : x) })))
  expect(within(card).queryByTestId('unread-dot')).toBeNull()
  expect(card.scrollWidth).toBeLessThanOrEqual(card.clientWidth)
})

it('keeps all pending decision kinds visible on the collapsed phone worker line', async () => {
  await page.viewport(390, 844)
  localStorage.clear()
  useMessageStore.setState({ messages: [] })
  useSessionStore.setState({ sessions: [makeSession({ id: 'main', status: 'idle' }), ...[3, 4, 5, 6].map(i => makeSession({ id: `worker-${i}`, title: `Decision worker ${i}`, parent_session_id: 'main', started_by: { kind: 'session' }, status: 'idle' }))] })
  render(<MemoryRouter><div style={{ height: 700 }}><SessionDockWindows sessionId="main" mobile files={null} /></div></MemoryRouter>)
  const workers = screen.getByRole('treeitem', { name: /^Workers/ })
  await waitFor(() => expect(workers).toHaveTextContent('Decision worker 6'))
  for (const i of [3, 4, 5, 6]) expect(workers).toHaveTextContent(`Decision worker ${i}`)
  expect(workers).toHaveAttribute('aria-expanded', 'false')
  expect(screen.queryByRole('treeitem', { name: 'Decision worker 3' })).toBeNull()
})
it('preserves standalone and orphan waiting signals without adding worker unread from nested channels', async () => {
  await page.viewport(390, 844)
  useMessageStore.setState({ messages: [] })
  const root = makeSession({ id: 'root', status: 'idle' })
  const standalone = makeSession({ id: 'standalone', title: 'Standalone', status: 'waiting', started_by: { kind: 'agent' }, unread: true })
  const orphan = makeSession({ id: 'orphan', title: 'Orphan', status: 'waiting', parent_session_id: 'missing', started_by: { kind: 'session' }, unread: true })
  const worker = makeSession({ id: 'managed', title: 'Managed', parent_session_id: 'root', started_by: { kind: 'session' }, unread: true })
  const channel = makeSession({ id: 'discussion', parent_session_id: 'managed', unread: true })
  useSessionStore.setState({ sessions: [root, standalone, orphan, worker, channel] })
  render(<MemoryRouter>{[standalone, orphan, worker].map(session => <div key={session.id} data-testid={`card-${session.id}`}><SessionCard session={session} daemon={undefined} /></div>)}</MemoryRouter>)
  for (const id of ['standalone', 'orphan']) {
    const card = screen.getByTestId(`card-${id}`)
    expect(within(card).getByTestId('unread-dot')).toBeVisible()
    expect(card.querySelector('.blerg-runner-waiting-flash')).not.toBeNull()
    expect(within(card).getByTestId('card-attention-count')).toHaveTextContent('1 needs you')
  }
  expect(within(screen.getByTestId('card-managed')).queryByTestId('unread-dot')).toBeNull()
})
