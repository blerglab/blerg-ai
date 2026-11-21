import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import { MemoryRouter, Route, Routes, useNavigate, useLocation } from 'react-router-dom'
import { useTranscriptStore } from '@blerglab/chat'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeSession, makeDaemon } from '../test/fixtures'
import { invalidateChatTransport } from '../lib/chatTransport'
import SessionDetail from './SessionDetail'
import CommandPaletteProvider from './CommandPalette'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
const main = makeSession({ id: 'main', title: 'Release workspace', kind: 'agent', status: 'idle' })
const channel = makeSession({ id: 'channel', title: 'Workout Planner', kind: 'agent', status: 'idle', parent_session_id: 'main' })
const other = makeSession({ id: 'other', title: 'Other workspace', kind: 'agent', status: 'idle' })
function Navigation() {
  const navigate = useNavigate()
  const location = useLocation()
  return <nav aria-label="Test workspace navigation"><button className="btn small" onClick={() => navigate('/sessions/main')}>Release workspace</button><button className="btn small" onClick={() => navigate('/sessions/other')}>Other workspace</button><button className="btn small" onClick={() => navigate('/sessions/channel')}>Open channel</button><output data-testid="route">{location.pathname}</output></nav>
}
function App({ path = '/sessions/main' }: { path?: string }) {
  return <MemoryRouter initialEntries={[path]}><CommandPaletteProvider><Navigation /><div style={{ height: '100vh' }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div></CommandPaletteProvider></MemoryRouter>
}
async function capture(width: number, viewer: 'deck' | 'diff') {
  if (!import.meta.env.VITE_CHANNEL_SHOTS_DIR) return
  const navigation = screen.getByRole('navigation', { name: 'Test workspace navigation' })
  navigation.hidden = true
  try {
    const { commands } = await import('vitest/browser')
    await commands.channelScreenshot(width, 'dark', viewer)
  } finally { navigation.hidden = false }
}
beforeEach(() => {
  localStorage.clear()
  document.documentElement.setAttribute('data-theme', 'dark')
  useTranscriptStore.setState({ sessions: {} })
  for (const session of [main, channel, other]) {
    useTranscriptStore.getState().ingest({ type: 'agent_event', session_id: session.id, client_event_id: `ready-${session.id}`, seq: 1, ts: '2026-10-25T12:00:00Z', kind: 'assistant_text', payload: { text: session.id === 'channel' ? 'Your workout plan is ready to review.' : 'Ready to review.', done: true } })
    useTranscriptStore.getState().ingestReplayDone(session.id, { lastSeq: 1, hasMore: false })
  }
  useSessionStore.getState().setInitialState([makeDaemon()], [main, channel, other])
  apiFetch.mockImplementation(async (url: string) => json(url.endsWith('/live') ? { session_id: url.split('/')[3], through_seq: 1, generated_at: new Date().toISOString(), tokens_hidden: false, asks: [], subagents: [], tasks: [], tool_changes: 0, counts: { needs_you: 0, running: 0 } } : url.endsWith('/shell-capability') ? { available: false } : {}))
  vi.stubGlobal('fetch', vi.fn(async (url: string) => json(String(url).endsWith('/artifacts') ? { artifacts: [] } : [])))
})
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals(); document.documentElement.removeAttribute('data-theme') })
for (const width of [390, 3840]) for (const removed of ['missing', 'archived']) {
  it(`restores a channel after workspace return and reload; ${removed === 'missing' ? 'drops a missing channel' : 'retains an archived channel read-only'} at ${width}px`, async () => {
    await page.viewport(width, width === 390 ? 844 : 2160)
    let app = render(<App />)
    fireEvent.click(screen.getByRole('button', { name: 'Open channel' }))
    const selected = () => screen.getAllByRole('tab').find(tab => tab.getAttribute('aria-selected') === 'true')
    await waitFor(() => expect(selected()?.textContent).toContain('Workout Planner'))
    if (removed === 'missing') await capture(width, 'deck')
    fireEvent.click(screen.getByRole('button', { name: 'Other workspace' }))
    await waitFor(() => expect(screen.getByTestId('route').textContent).toBe('/sessions/other'))
    fireEvent.click(screen.getByRole('button', { name: 'Release workspace' }))
    await waitFor(() => expect(selected()?.textContent).toContain('Workout Planner'))
    await waitFor(() => expect(screen.getByTestId('route').textContent).toBe('/sessions/channel'))
    app.unmount()
    app = render(<App />)
    await waitFor(() => expect(selected()?.textContent).toContain('Workout Planner'))
    await waitFor(() => expect(screen.getByTestId('route').textContent).toBe('/sessions/channel'))
    if (removed === 'missing') await capture(width, 'diff')
    app.unmount()
    act(() => useSessionStore.setState({ sessions: removed === 'missing' ? [main, other] : [main, other, { ...channel, archived_at: '2026-11-01T00:00:00Z' }] }))
    render(<App />)
    await waitFor(() => expect(selected()?.textContent).toContain(removed === 'missing' ? 'Main' : 'Workout Planner'))
    if (removed === 'archived') expect(await screen.findByText(/Archived workspace.*read only/)).toBeTruthy()
    expect(screen.queryByText('This workspace is unavailable.')).toBeNull()
  })
}

for (const width of [390, 3840]) {
  for (const archived of [false, true]) it(`deep links override a remembered tab (${archived ? 'archived' : 'live'}) at ${width}px`, async () => {
    await page.viewport(width, width === 390 ? 844 : 2160)
    useSessionStore.getState().setInitialState([makeDaemon()], [main, other, { ...channel, archived_at: archived ? '2026-11-01T00:00:00Z' : null }])
    localStorage.setItem('blerg-runner.tabs.person-a.main', JSON.stringify({ tabs: [{ id: 'session-chat:other', preview: false, view: { kind: 'session-chat', sessionId: 'other', label: other.title } }], active: 'session-chat:other' }))
    render(<App path="/sessions/channel" />)
    await waitFor(() => expect(screen.getAllByRole('tab').find(tab => tab.getAttribute('aria-selected') === 'true')?.textContent).toContain('Workout Planner'))
    expect(screen.getByTestId('route').textContent).toBe('/sessions/channel')
    if (archived) expect(await screen.findByText(/Archived workspace.*read only/)).toBeTruthy()
    if (archived && import.meta.env.VITE_ARCHIVED_TAB_SHOTS) await capture(width, 'deck')
  })
  it(`retains a remembered channel through a partial reconnect inventory at ${width}px`, async () => {
    await page.viewport(width, width === 390 ? 844 : 2160)
    const saved = { tabs: [{ id: 'session-chat:channel', preview: false, view: { kind: 'session-chat', sessionId: 'channel', label: channel.title } }], active: 'session-chat:channel' }
    localStorage.setItem('blerg-runner.tabs.person-a.main', JSON.stringify(saved))
    useSessionStore.setState({ sessions: [main, other], inventoryComplete: false })
    render(<App />)
    expect(screen.getByRole('tab', { name: /Workout Planner/ })).toBeTruthy()
    expect(JSON.parse(localStorage.getItem('blerg-runner.tabs.person-a.main')!).tabs.some((tab: { id: string }) => tab.id === 'session-chat:channel')).toBe(true)
    act(() => useSessionStore.getState().setInitialState([makeDaemon()], [main, channel, other]))
    await waitFor(() => expect(screen.getByTestId('route').textContent).toBe('/sessions/channel'))
    expect(screen.getAllByRole('tab').find(tab => tab.getAttribute('aria-selected') === 'true')?.textContent).toContain('Workout Planner')
  })
}
