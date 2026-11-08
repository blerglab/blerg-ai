// Real SessionDetail, shipped styles, transport-shaped replies, and server-page navigation.
import { afterEach, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import CommandPaletteProvider from './CommandPalette'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { invalidateChatTransport } from '../lib/chatTransport'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'
import { setTheme } from '../../../../packages/settings/theme'
import '../index.css'
import '@blerglab/chat/blerg.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals(); vi.restoreAllMocks() })

it.each([
  { width: 390, pane: 390, theme: 'dark' as const },
  { width: 3840, pane: 1024, theme: 'dark' as const },
  { width: 3840, pane: 3840, theme: 'dark' as const },
  { width: 3840, pane: 1024, theme: 'colorblind-dark' as const },
])('pages History in the real app at $width px, $pane px pane, $theme', async ({ width, pane, theme }) => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  localStorage.clear()
  setTheme(theme)
  localStorage.setItem(RAIL_PREF_KEY, '1')
  localStorage.setItem(RAIL_TAB_KEY, 'history')
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ id: 'history-main', kind: 'agent', status: 'idle', title: 'Release coordination' })], serverVersion: 'test' })
  useTranscriptStore.setState({ sessions: {} })
  const store = useTranscriptStore.getState()
  store.ingest({ type: 'agent_event', session_id: 'history-main', client_event_id: 'request', seq: 1, ts: new Date().toISOString(), kind: 'user_message', payload: { text: 'Coordinate the release checks and record the important decisions.', source: 'chat' } } as AgentEvent)
  store.ingestReplayDone('history-main', { lastSeq: 1, hasMore: false })
  const reply = (older: boolean) => ({
    session_id: 'history-main', through_seq: 20000, generated_at: new Date().toISOString(), tokens_hidden: false,
    topics: [], unmarked: null, ...(older ? {} : { next_cursor: '20000:19701' }),
    strokes: Array.from({ length: 6 }, (_, i) => ({
      id: (older ? 'older-' : 'newest-') + i, kind: 'decision', source: 'mark', by: 'agent',
      text: (older ? 'Earlier decision' : 'Release decision') + ': ' + ['Keep the review record', 'Run focused checks before handoff', 'Preserve the release notes', 'Wait for the gate result', 'Use the recorded session branch', 'Record the deployment outcome'][i],
      seq: (older ? 19700 : 20000) - i, at: new Date(Date.now() - (older ? 86400_000 : 0) - i * 3600_000).toISOString(), jump: null,
    })),
  })
  apiFetch.mockReset().mockImplementation(async (url: string) => {
    if (url.includes('/topics')) return json(reply(url.includes('cursor=')))
    if (url.includes('/live')) return json({ can_release_prompt: false })
    if (url.endsWith('/permissions')) return json({ can_grant: false, reason: '', grants: [], mcp_tools: [], boards: [] })
    if (url.includes('/workspace?')) return json({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: [], truncated: false }, members: [], runtime: 'host' })
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn(async () => json([])))
  const view = render(<MemoryRouter initialEntries={['/sessions/history-main']}><CommandPaletteProvider>
    <div style={{ display: 'flex', height: '100vh', width: pane, maxWidth: '100%' }}>
      <div style={{ flex: 1, minWidth: 0 }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div>
    </div>
  </CommandPaletteProvider></MemoryRouter>)
  if (width === 390) await userEvent.click(await screen.findByTestId('rail-toggle'))
  const older = await screen.findByRole('button', { name: 'Show older history' })
  await waitFor(() => expect(older.getBoundingClientRect().width).toBeGreaterThan(0))
  const shots = import.meta.env.VITE_POPOUT_SHOTS_DIR
  const capture = async (state: string) => {
    if (!shots) return
    const frame = document.createElement('div')
    frame.style.cssText = 'position:fixed;inset:0;width:100vw;height:100vh;pointer-events:none'
    document.body.append(frame)
    expect(frame.getBoundingClientRect().width).toBe(width)
    try { await page.screenshot({ element: frame, path: `${shots}/history-${state}-${width}-pane-${pane}-${theme}.png` }) } finally { frame.remove() }
  }
  await capture('newest')
  await userEvent.click(older)
  await screen.findByText(/Earlier decision: Keep the review record/)
  expect(apiFetch.mock.calls.some(([url]) => String(url).endsWith('/topics?cursor=20000%3A19701'))).toBe(true)
  expect(screen.queryByRole('button', { name: 'Show older history' })).toBeNull()
  expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Show newer history' }))
  await capture('older')
  await userEvent.click(screen.getByRole('button', { name: 'Show newer history' }))
  await screen.findByText(/Release decision: Keep the review record/)
  view.unmount()
})
