import { browserWidths } from '../test/browserWidths'
// Escalated child questions stay on their original session while the owner answers from the parent.
// Check parent and global Needs you in real browsers at phone and wide desktop sizes.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import { commands, page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import '../index.css'
import { seedRichRail } from '../test/railFixture'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async (orig) => ({
  ...(await orig<typeof import('../authClient')>()),
  getAccessToken: () => 'tok',
  redirectToRefresh: vi.fn(),
}))
vi.mock('../claims', async (orig) => ({ ...(await orig<typeof import('../claims')>()), tokenExpired: () => false }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

import SessionDetail from './SessionDetail'

import { ProposalBell } from './ProposalNotifications'
import { useMessageStore } from '../hooks/useMessageStore'
import { LIVE_MIN_GAP_MS } from '../hooks/useLiveStateServer'
import { useSessionStore } from '../hooks/useSessionStore'
import { useToastStore } from '../hooks/useToastStore'
import { makeSession } from '../test/fixtures'
import { emit } from '../test/wsMock'

const question = { id: 'child-question', session_id: 'child-worker', kind: 'ask' as const,
  status: 'open' as const, body: 'Which release branch should I use?', created_at: new Date().toISOString(),
  owner_only: true, answerable: true }
const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }))
let parentHandling = false
let answered = false
let nonblocking = false
let waiterEnded = false
beforeEach(() => {
  localStorage.removeItem(RAIL_PREF_KEY)
  localStorage.setItem(RAIL_TAB_KEY, 'live')
  parentHandling = false
  answered = false
  nonblocking = false
  waiterEnded = false
  const { live } = seedRichRail()
  live.can_release_prompt = true
  live.child_asks = [{ message_id: question.id, session_id: question.session_id, child_title: 'Release worker', question: question.body, asked_at: question.created_at, owner_only: true }]
  useSessionStore.setState(s => ({ sessions: [...s.sessions, makeSession({ id: question.session_id, title: 'Release worker', parent_session_id: 'sess-a' })] }))
  useMessageStore.setState({ messages: [question] })
  useToastStore.setState({ toasts: [] })
  apiFetch.mockImplementation((url: string, init?: RequestInit) => {
    if (url === `/api/messages/${question.id}/reply` && init?.method === 'POST') { answered = true; return json({ id: question.id }) }
    return json(url.endsWith('/live') ? { ...live, child_asks: answered || parentHandling ? [] : live.child_asks?.map(q => ({ ...q, no_wait: nonblocking || waiterEnded, waiter_ended: waiterEnded })) } : url.startsWith('/api/tool-changes') ? { tool_changes: [] } : {})
  })
  vi.stubGlobal('fetch', vi.fn(() => json([])))
})
afterEach(() => {
  vi.unstubAllGlobals()
  useMessageStore.setState({ messages: [] })
  localStorage.removeItem(RAIL_PREF_KEY)
  localStorage.removeItem(RAIL_TAB_KEY)
})

it.each(browserWidths([390, 3840]))('marks a person-only question the child stopped waiting on at %i px', async (width) => {
  waiterEnded = true
  useMessageStore.setState({ messages: [{ ...question, no_wait: true, waiter_ended: true }] })
  await page.viewport(width, 1000)
  render(<div style={{ height: '100vh' }}><MemoryRouter initialEntries={['/sessions/sess-a']}>
    <ProposalBell inbox={{ pending: [], error: '', refresh: async () => {}, clockOffset: 0 }} />
    <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
  </MemoryRouter></div>)
  if (width < 900) await userEvent.click(screen.getByTestId('rail-toggle'))
  const rail = await screen.findByTestId('session-rail')
  const card = (await within(within(rail).getByTestId('rail-needs')).findByRole('button', { name: /Which release branch/ })).closest('li')!
  await userEvent.click(within(card).getByRole('button', { expanded: false }))
  const dialog = screen.getAllByRole('dialog').at(-1)!
  const notice = within(dialog).getByTitle(/The agent stopped waiting. Your answer will be sent as a message./)
  expect(notice).toBeVisible()
  expect(notice).toHaveAttribute('title', expect.stringContaining('The agent stopped waiting.'))
  expect(dialog.textContent).toContain(question.body)
  expect(within(dialog).getByRole('button', { name: /answer|send/i })).toBeTruthy()
  expect(card.scrollWidth).toBeLessThanOrEqual(card.clientWidth + 1)
  const shots = import.meta.env.VITE_QUESTION_SHOT_DIR
  if (shots) await commands.questionScreenshot(width)
})

it.each([[390, false], [390, true], [3840, false], [3840, true]] as const)('answers the named child from the parent and removes it from both Needs-you surfaces at %i px (no-wait %s)', async (width, noWait) => {
  nonblocking = noWait
  useMessageStore.setState({ messages: [{ ...question, no_wait: noWait }] })
  await page.viewport(width, 1000)
  const view = render(<div style={{ height: '100vh' }}><MemoryRouter initialEntries={['/sessions/sess-a']}>
    <ProposalBell inbox={{ pending: [], error: '', refresh: async () => {}, clockOffset: 0 }} />
    <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
  </MemoryRouter></div>)
  if (width < 900) await userEvent.click(screen.getByTestId('rail-toggle'))
  const rail = await screen.findByTestId('session-rail')
  const row = await within(within(rail).getByTestId('rail-needs')).findByRole('button', { name: /Which release branch/ })
  const card = row.closest('li')!
  expect(card.textContent).toContain(question.body)
  await userEvent.click(within(card).getByRole('button', { expanded: false }))
  const dialog = screen.getAllByRole('dialog').at(-1)!
  expect(within(dialog).getByRole('link', { name: 'Open worker' }).getAttribute('href')).toBe('/sessions/child-worker')
  expect(card.scrollWidth).toBeLessThanOrEqual(card.clientWidth + 1)
  await waitFor(() => {
    const box = card.getBoundingClientRect()
    expect(box.left).toBeGreaterThanOrEqual(0)
    expect(box.right).toBeLessThanOrEqual(width)
  })
  // Replayed creation must not add another row or toast.
  act(() => emit({ type: 'message_created', message: question }))
  expect(useToastStore.getState().toasts).toHaveLength(0)
  expect(within(rail).getAllByText(question.body)).toHaveLength(1)
  const answer = within(dialog).getByRole('textbox', { name: 'Your answer' })
  await userEvent.fill(answer, 'Use the release branch.')
  await userEvent.click(within(dialog).getByRole('button', { name: 'Answer' }))
  await waitFor(() => expect(answered).toBe(true))
  await waitFor(() => expect(within(rail).queryByText(question.body)).toBeNull())
  expect(apiFetch).toHaveBeenCalledWith(`/api/messages/${question.id}/reply`, expect.objectContaining({ body: JSON.stringify({ answer: 'Use the release branch.', revision_seq: 0 }) }))
  // Navigate away from the session; the global surface must keep the answer.
  view.unmount()
  render(<MemoryRouter><ProposalBell inbox={{ pending: [], error: '', refresh: async () => {}, clockOffset: 0 }} /></MemoryRouter>)
  await userEvent.click(screen.getByTestId('notifications-bell'))
  expect(screen.queryByTestId('bell-question')).toBeNull()
})

it.each(browserWidths([390, 3840]))('lists a blocking child question once in global Needs you at %i px', async width => {
  await page.viewport(width, 1000)
  render(<MemoryRouter><ProposalBell inbox={{ pending: [], error: '', refresh: async () => {}, clockOffset: 0 }} /></MemoryRouter>)
  await userEvent.click(screen.getByTestId('notifications-bell'))
  const rows = screen.getAllByTestId('bell-question')
  expect(rows).toHaveLength(1)
  expect(rows[0].textContent).toContain('Release worker')
  expect(rows[0].textContent).toContain(question.body)
  expect(rows[0].scrollWidth).toBeLessThanOrEqual(rows[0].clientWidth + 1)
  const box = rows[0].getBoundingClientRect()
  expect(box.left).toBeGreaterThanOrEqual(0)
  expect(box.right).toBeLessThanOrEqual(width)
})


it.each(browserWidths([390, 3840]))('keeps a question handled by the parent agent out of both Needs-you surfaces at %i px', async width => {
  parentHandling = true
  useMessageStore.setState({ messages: [{ ...question, answerable: false }] })
  await page.viewport(width, 1000)
  const view = render(<div style={{ height: '100vh' }}><MemoryRouter initialEntries={['/sessions/sess-a']}>
    <ProposalBell inbox={{ pending: [], error: '', refresh: async () => {}, clockOffset: 0 }} />
    <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
  </MemoryRouter></div>)
  if (width < 900) await userEvent.click(screen.getByTestId('rail-toggle'))
  const rail = await screen.findByTestId('session-rail')
  await waitFor(() => expect(apiFetch).toHaveBeenCalledWith(expect.stringContaining('/live'), expect.anything()))
  expect(within(within(rail).getByTestId('rail-needs')).queryByRole('button', { name: /Which release branch/ })).toBeNull()
  act(() => emit({ type: 'message_created', message: { ...question, answerable: false }, quiet: true }))
  expect(useToastStore.getState().toasts).toHaveLength(0)
  view.unmount()
  render(<MemoryRouter><ProposalBell inbox={{ pending: [], error: '', refresh: async () => {}, clockOffset: 0 }} /></MemoryRouter>)
  await userEvent.click(screen.getByTestId('notifications-bell'))
  expect(screen.queryByTestId('bell-question')).toBeNull()
})

it.each(browserWidths([390, 3840]))('updates an open parent and an open bell on escalation at %i px', async width => {
  parentHandling = true
  useMessageStore.setState({ messages: [{ ...question, answerable: false }] })
  await page.viewport(width, 1000)
  const view = render(<div style={{ height: '100vh' }}><MemoryRouter initialEntries={['/sessions/sess-a']}>
    <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
  </MemoryRouter></div>)
  if (width < 900) await userEvent.click(screen.getByTestId('rail-toggle'))
  const rail = await screen.findByTestId('session-rail')
  await waitFor(() => expect(apiFetch).toHaveBeenCalledWith(expect.stringContaining('/live'), expect.anything()))
  expect(within(within(rail).getByTestId('rail-needs')).queryByRole('button', { name: /Which release branch/ })).toBeNull()
  parentHandling = false
  act(() => emit({ type: 'message_created', message: question }))
  // A trailing live-state read honors the same minimum gap as every panel refresh.
  await waitFor(() => expect(within(within(rail).getByTestId('rail-needs')).getByRole('button', { name: /Which release branch/ })).toBeTruthy(), { timeout: LIVE_MIN_GAP_MS + 2000 })
  expect(useToastStore.getState().toasts).toHaveLength(1)
  act(() => emit({ type: 'message_created', message: question }))
  expect(useToastStore.getState().toasts).toHaveLength(1)
  // The phone session fills its viewport; exercise the global bell in its own open viewport.
  view.unmount()
  useMessageStore.setState({ messages: [{ ...question, answerable: false }] })
  useToastStore.setState({ toasts: [] })
  render(<MemoryRouter><ProposalBell inbox={{ pending: [], error: '', refresh: async () => {}, clockOffset: 0 }} /></MemoryRouter>)
  await userEvent.click(screen.getByTestId('notifications-bell'))
  expect(screen.queryByTestId('bell-question')).toBeNull()
  act(() => emit({ type: 'message_created', message: question }))
  expect(await screen.findByTestId('bell-question')).toBeTruthy()
  expect(useToastStore.getState().toasts).toHaveLength(1)
  act(() => emit({ type: 'message_created', message: question }))
  expect(useToastStore.getState().toasts).toHaveLength(1)
})
