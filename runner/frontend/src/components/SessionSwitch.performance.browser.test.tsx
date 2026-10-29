// #250: time from switching back to a session to its first explorer row, with the explorer and
// panel cache (drawn from memory) and without it (the cache dropped: every part waits for its
// read, as before #250). The fake server answers each read after SERVER_MS, as a daemon does.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import { useEffect } from 'react'
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom'
import { commands, page } from 'vitest/browser'
import { useTranscriptStore } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import { useSessionStore } from '../hooks/useSessionStore'
import { explorerPrefKey } from '../hooks/useExplorerLayout'
import { makeDaemon, makeSession } from '../test/fixtures'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'
import { invalidateChatTransport } from '../lib/chatTransport'
import { dropView } from '../lib/sessionViewCache'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
const timingsFile = (import.meta.env as Record<string, string | undefined>).VITE_SWITCH_TIMINGS

/** A daemon under load: the explorer's reads wait their turn (one per person at a time). */
const SERVER_MS = 250
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
const entries = Array.from({ length: 120 }, (_, i) => ({ name: `file-${i}.ts`, path: `file-${i}.ts`, directory: false }))
beforeEach(() => {
  useTranscriptStore.setState({ sessions: {} })
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: ['a', 'b'].map(x => makeSession({ id: `sess-${x}`, kind: 'agent', status: 'idle', title: x })) })
  localStorage.setItem(RAIL_PREF_KEY, '1')
  localStorage.setItem(RAIL_TAB_KEY, 'live')
  localStorage.setItem(explorerPrefKey('person-a'), JSON.stringify({ open: true, width: 320 }))
  apiFetch.mockReset().mockImplementation(async (url: string) => {
    await new Promise(r => setTimeout(r, SERVER_MS))
    if (url.includes('/workspace?')) return json({ result: { ok: true, launch_generation: 'launch', generation: 1, entries, truncated: false }, members: [], runtime: 'host' })
    if (url.endsWith('/permissions')) return json({ can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [] })
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn(async () => json([])))
})
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals() })

const nav: { go: (path: string) => void } = { go: () => {} }
function Nav() { const navigate = useNavigate(); useEffect(() => { nav.go = navigate }, [navigate]); return null }
const go = (path: string) => nav.go(path)
const row = () => document.querySelector('.explorer-dock [role="treeitem"]')
const permissions = () => screen.queryByText('None yet: this session asks you each time.')

async function switchBack(cached: boolean): Promise<{ row: number; panel: number }> {
  await act(async () => go('/sessions/sess-b'))
  await waitFor(() => expect(row()).not.toBeNull())
  await act(async () => { await new Promise(r => setTimeout(r, 4 * SERVER_MS)) })
  if (!cached) dropView('sess-a')
  const start = performance.now()
  let firstRow = 0, panel = 0
  act(() => go('/sessions/sess-a'))
  await waitFor(() => {
    if (!firstRow && row()) firstRow = performance.now() - start
    if (!panel && permissions()) panel = performance.now() - start
    expect(firstRow && panel).toBeTruthy()
  }, { interval: 2, timeout: 5000 })
  await act(async () => { await new Promise(r => setTimeout(r, 4 * SERVER_MS)) })
  return { row: firstRow, panel }
}

it('switching back draws the first explorer row and the panel from memory, not after the reads', async () => {
  await page.viewport(1440, 900)
  render(<MemoryRouter initialEntries={['/sessions/sess-a']}><Nav /><div style={{ height: '100vh' }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div></MemoryRouter>)
  await waitFor(() => expect(row()).not.toBeNull())
  await within(screen.getByTestId('session-rail')).findByText('None yet: this session asks you each time.')
  const before: { row: number; panel: number }[] = [], after: { row: number; panel: number }[] = []
  for (let i = 0; i < 3; i++) {
    before.push(await switchBack(false))
    after.push(await switchBack(true))
  }
  const median = (xs: number[]) => [...xs].sort((a, b) => a - b)[Math.floor(xs.length / 2)]
  const result = {
    serverMs: SERVER_MS,
    before: { firstRowMs: median(before.map(x => x.row)), panelMs: median(before.map(x => x.panel)) },
    after: { firstRowMs: median(after.map(x => x.row)), panelMs: median(after.map(x => x.panel)) },
  }
  if (timingsFile) await commands.writeFile(timingsFile, JSON.stringify(result, null, 2))
  // Without the cache the row waits for the listing; with it, it is there before any read answers.
  expect(result.before.firstRowMs).toBeGreaterThanOrEqual(SERVER_MS)
  expect(result.before.panelMs).toBeGreaterThanOrEqual(SERVER_MS)
  expect(result.after.firstRowMs).toBeLessThan(SERVER_MS)
  expect(result.after.panelMs).toBeLessThan(SERVER_MS)
}, 60_000)
