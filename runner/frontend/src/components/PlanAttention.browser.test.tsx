import { act, render, screen, within, waitFor } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { seedRichRail } from '../test/railFixture'
import { makeSession } from '../test/fixtures'
import { useSessionStore } from '../hooks/useSessionStore'
import { useMessageStore } from '../hooks/useMessageStore'
import { RAIL_TAB_KEY } from '../lib/sessionRail'
import { setTheme } from '../../../../packages/settings/theme'
import '../index.css'
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async orig => ({ ...(await orig<typeof import('../authClient')>()), getAccessToken: () => 'tok', redirectToRefresh: vi.fn() }))
vi.mock('../claims', async orig => ({ ...(await orig<typeof import('../claims')>()), tokenExpired: () => false }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
import SessionDetail from './SessionDetail'
import SessionCard from './SessionCard'
const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } }))
let pending = true
beforeEach(() => {
  localStorage.clear()
  localStorage.setItem(RAIL_TAB_KEY, 'live')
  pending = true
  const { live } = seedRichRail()
  useSessionStore.setState(s => ({ sessions: [...s.sessions, makeSession({ id: 'failed', title: 'Build worker', parent_session_id: 'sess-a', started_by: { kind: 'session' }, status: 'error' }), makeSession({ id: 'paused', title: 'Paused worker', parent_session_id: 'sess-a', started_by: { kind: 'session' }, status: 'idle', paused_reason: 'restart' })] }))
  useMessageStore.setState({ messages: ['First question?', 'Second question?'].map((body, i) => ({ id: `ask-${i}`, session_id: 'sess-a', kind: 'ask', status: 'open', answerable: true, body, created_at: '2026-01-01T00:00:00Z' })) })
  apiFetch.mockImplementation((url: string) => json(
    url.startsWith('/api/blackboards?') ? { blackboards: [{ id: 'plan', name: 'Next release', milestones: [] }] }
      : url.startsWith('/api/blackboards/plan?') ? { record: { proposals: pending ? [{ id: 'proposal', milestone_id: '', revision: 2, status: 'pending', changes: [{ id: 'change', operation: 'add', item: { title: 'Keep counts consistent', reason: 'Bug' } }] }] : [] } }
      : url.endsWith('/live') ? { ...live, session_id: url.split('/')[3], tool_changes: 0, asks: [], review_requests: url.includes('/sess-a/') ? [{ request_id: 'r', artifact_id: 'file', artifact_name: 'Notes.md', artifact_version: 1, ask: 'Review notes' }] : [] }
      : url.endsWith('/permissions') ? { can_grant: true, grants: [], boards: [], mcp_tools: [] }
      : url.startsWith('/api/models') ? { models: [] } : {}))
  vi.stubGlobal('fetch', vi.fn(() => json([])))
})
for (const width of [390, 3840]) it(`keeps both lists, the badge and card equal for all attention kinds at ${width}px`, async () => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  setTheme(width === 390 ? 'dark' : 'colorblind-dark')
  const root = useSessionStore.getState().sessions[0]
  render(<MemoryRouter initialEntries={['/sessions/sess-a']}><div data-testid="workspace-card"><SessionCard session={root} daemon={undefined} /></div><div style={{ height: '80vh' }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div></MemoryRouter>)
  await waitFor(() => expect(within(screen.getByTestId('workspace-card')).getByTestId('card-attention-count')).toHaveTextContent('1 failed, 5 need you'))
  if (width === 390) await userEvent.click(screen.getByTestId('rail-toggle'))
  const needs = await screen.findByTestId('rail-needs')
  await waitFor(() => expect(needs.querySelectorAll('.attention-row')).toHaveLength(3))
  expect(within(needs).getByText('6', { exact: true })).toBeVisible()
  await userEvent.click(within(needs).getByRole('button', { name: 'Open Needs you as tab' }))
  const fullNeeds = await screen.findByRole('region', { name: 'Needs you' })
  await waitFor(() => expect(within(fullNeeds).getAllByRole('listitem')).toHaveLength(6))
  await userEvent.click(within(fullNeeds).getByRole('button', { name: 'Review plan · 1 change' }))
  expect(await screen.findByRole('dialog', { name: 'Plan changes for Next release' })).toHaveTextContent('Keep counts consistent')
  await userEvent.click(screen.getByRole('button', { name: 'Close plan review' }))
  const shots = import.meta.env.VITE_QUESTION_SHOT_DIR
  const shot = async (surface: string) => {
    if (!shots) return
    const frame = document.createElement('div')
    frame.style.cssText = 'position:fixed;inset:0;width:100vw;height:100vh;pointer-events:none'
    document.body.append(frame)
    try { await page.screenshot({ element: frame, path: `${shots}/plan-${surface}-${width}.png` }) } finally { frame.remove() }
  }
  await shot('needs-you')
  await userEvent.click(screen.getByRole('button', { name: 'Toggle explorer' }))
  await userEvent.click(await screen.findByRole('tab', { name: 'Questions' }))
  const queue = screen.getByRole('region', { name: 'Question queue' })
  expect(queue.querySelectorAll('.attention-row')).toHaveLength(6)
  expect(screen.getByRole('tab', { name: 'Questions' })).toHaveAttribute('aria-description', '6 open questions')
  expect(queue.scrollWidth).toBeLessThanOrEqual(queue.clientWidth + 1)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  await shot('questions')
  await userEvent.click(within(queue).getByRole('button', { name: 'Review plan · 1 change' }))
  expect(await screen.findByRole('dialog', { name: 'Plan changes for Next release' })).toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: 'Close plan review' }))
  pending = false
  act(() => window.dispatchEvent(new Event('blerg:proposals-changed')))
  await waitFor(() => expect(queue.querySelectorAll('.attention-row')).toHaveLength(5))
  expect(screen.getByRole('tab', { name: 'Questions' })).toHaveAttribute('aria-description', '5 open questions')
  await waitFor(() => expect(within(fullNeeds).getAllByRole('listitem')).toHaveLength(5))
  expect(within(screen.getByTestId('workspace-card')).getByTestId('card-attention-count')).toHaveTextContent('1 failed, 4 need you')
})
