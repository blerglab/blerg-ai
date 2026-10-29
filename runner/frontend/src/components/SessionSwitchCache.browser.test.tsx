// #250: switching back to a session draws its explorer and session panel at once from memory, then
// reads again in the background, one explorer read at a time, without moving expansion, scroll or
// focus. A runtime change discards the cached pages; another account sees nothing of them; a failed
// read keeps the cached view and says so; Refresh still reads everything again.
import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { act, render, screen, within } from '@testing-library/react'
import { useEffect } from 'react'
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom'
import { page, userEvent } from 'vitest/browser'
import { useTranscriptStore } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import CommandPaletteProvider from './CommandPalette'
import { useSessionStore } from '../hooks/useSessionStore'
import { explorerPrefKey } from '../hooks/useExplorerLayout'
import { makeDaemon, makeSession } from '../test/fixtures'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'
import { invalidateChatTransport } from '../lib/chatTransport'
import { cachedViewSessions, readView } from '../lib/sessionViewCache'
import type { ExplorerSnapshot } from './WorkspaceTree'
import '../index.css'
import '@blerglab/chat/blerg.css'

const who = vi.hoisted(() => ({ subject: 'person-a' }))
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: who.subject, exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_SWITCH_SHOT_DIR

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const files = (prefix: string, n: number) => Array.from({ length: n }, (_, i) => ({ name: `${prefix}-${String(i).padStart(2, '0')}.ts`, path: `${prefix}-${String(i).padStart(2, '0')}.ts`, directory: false }))

/** The fake server: per-session generation, a gate that holds reads, failures and what was asked. */
const server = {
  generation: {} as Record<string, number>,
  hold: false,
  held: [] as (() => void)[],
  fail: new Set<string>(),
  calls: [] as string[],
  workspaceInFlight: 0,
  workspaceMaxInFlight: 0,
}
function rootFor(id: string) {
  const fresh = (server.generation[id] ?? 1) > 1
  return [
    { name: 'app', path: 'app', directory: true, repository: true, member_id: 'app' },
    { name: 'docs', path: 'docs', directory: true },
    ...(fresh ? [{ name: 'FRESH.md', path: 'FRESH.md', directory: false }] : [{ name: `README-${id}.md`, path: 'README.md', directory: false }]),
    ...files('file', 200),
  ]
}
function workspace(id: string, url: string) {
  const query = new URL(url, 'https://example.com').searchParams
  const path = query.get('path') ?? '', op = query.get('op')
  const entries = op === 'search' ? [{ name: 'README.md', path: 'README.md', directory: false }]
    : path === 'docs' ? [{ name: `guide-${id}.md`, path: 'docs/guide.md', directory: false }]
      : path === 'app' ? [{ name: 'index.ts', path: 'app/index.ts', directory: false }]
        : rootFor(id)
  return json({ result: { ok: true, launch_generation: 'launch', generation: server.generation[id] ?? 1, entries, truncated: false, ...(op === 'status' ? { status: { branch: 'main', changed: 1, untracked: 0, files: [] } } : {}) }, members: [{ id: 'app', name: 'app', path: 'app' }], runtime: 'host' })
}
const topics = (id: string) => ({ session_id: id, through_seq: 2, generated_at: '2026-10-27T12:00:00Z', tokens_hidden: false, unmarked: null, strokes: [],
  topics: [{ id: 't1', source: 'marker', title: `Cache the explorer for ${id}`, status: 'open', start_seq: 1, end_seq: 2, started_at: '2026-10-27T11:00:00Z', ended_at: null, counts: {}, items: [] }] })

beforeEach(() => {
  who.subject = 'person-a'
  Object.assign(server, { generation: {}, hold: false, held: [], fail: new Set(), calls: [], workspaceInFlight: 0, workspaceMaxInFlight: 0 })
  useTranscriptStore.setState({ sessions: {} })
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: ['a', 'b'].map(x => makeSession({ id: `sess-${x}`, kind: 'agent', status: 'idle', title: `Session ${x.toUpperCase()}` })) })
  localStorage.setItem(RAIL_PREF_KEY, '1')
  localStorage.setItem(RAIL_TAB_KEY, 'live')
  for (const subject of ['person-a', 'person-b']) localStorage.setItem(explorerPrefKey(subject), JSON.stringify({ open: true, width: 320 }))
  apiFetch.mockReset().mockImplementation(async (url: string) => {
    server.calls.push(url)
    const id = /\/api\/sessions\/([^/]+)\//.exec(url)?.[1] ?? ''
    const isWorkspace = url.includes('/workspace?')
    if (isWorkspace) server.workspaceMaxInFlight = Math.max(server.workspaceMaxInFlight, ++server.workspaceInFlight)
    try {
      if (server.hold && /\/(workspace\?|topics|permissions|plan-nudges)/.test(url)) await new Promise<void>(resolve => server.held.push(resolve))
      if ([...server.fail].some(part => url.includes(part))) return json({ error: 'The daemon is not answering.' }, 500)
      if (isWorkspace) return workspace(id, url)
      if (url.endsWith('/topics')) return json(topics(id))
      if (url.endsWith('/permissions')) return json({ can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [] })
      if (url.endsWith('/plan-nudges')) return json({ enabled: true })
      if (url.startsWith('/api/tool-changes')) return json({ tool_changes: [] })
      return json({})
    } finally {
      if (isWorkspace) server.workspaceInFlight--
    }
  })
  vi.stubGlobal('fetch', vi.fn(async (url: string) => json(String(url).endsWith('/artifacts') ? { artifacts: [] } : [])))
})
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals() })

const nav: { go: (path: string) => void } = { go: () => {} }
function Nav() { const navigate = useNavigate(); useEffect(() => { nav.go = navigate }, [navigate]); return null }
const go = (path: string) => nav.go(path)
function mount() {
  return render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider><Nav />
    <div style={{ display: 'flex', height: '100vh' }}><div style={{ flex: 1, minWidth: 0 }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div></div>
  </CommandPaletteProvider></MemoryRouter>)
}
const explorer = () => screen.getByRole('region', { name: 'Explorer' })
const tree = () => within(explorer()).getByRole('tree', { name: 'Workspace files' })
const body = () => explorer().querySelector<HTMLElement>('.explorer-body')!
const panel = () => screen.getByTestId('session-rail')
const workspaceCalls = () => server.calls.filter(u => u.includes('/workspace?')).map(u => { const q = new URL(u, 'https://x').searchParams; return `${q.get('op')}:${q.get('path')}` })
/** Lets every held read go, until none is left (each released read may start the next). */
async function release() {
  server.hold = false
  for (let i = 0; i < 50 && server.held.length; i++) await act(async () => { server.held.splice(0).forEach(r => r()); await new Promise(r => setTimeout(r, 20)) })
}
async function settle() { await act(async () => { await new Promise(r => setTimeout(r, 300)) }) }
async function visitA() {
  await within(explorer()).findByRole('treeitem', { name: /^README-sess-a\.md/ })
  await userEvent.click(within(tree()).getByRole('treeitem', { name: /^docs/ }))
  await within(tree()).findByRole('treeitem', { name: /^guide-sess-a\.md/ })
  await within(panel()).findByText('None yet: this session asks you each time.')
  await within(panel()).findByRole('checkbox', { name: 'Nudge when the plan drifts' })
  body().scrollTop = 400
  body().dispatchEvent(new Event('scroll'))
  await settle()
}
async function toB() {
  await act(async () => go('/sessions/sess-b'))
  await within(explorer()).findByRole('treeitem', { name: /^README-sess-b\.md/ })
  await settle()
}

for (const [width, height] of [[1440, 900], [3840, 2160]] as const) {
  it(`A→B→A draws A's explorer and panel at once, then reads again only what is visible at ${width}`, async () => {
    await page.viewport(width, height)
    mount()
    await visitA()
    await toB()
    server.calls.length = 0
    server.hold = true
    const shown = performance.now()
    await act(async () => go('/sessions/sess-a'))
    // In the first paint, before any read has answered: rows, expansion, scroll, panel content.
    expect(performance.now() - shown).toBeLessThan(1000)
    expect(within(tree()).getByRole('treeitem', { name: /^README-sess-a\.md/ })).toBeTruthy()
    expect(within(tree()).getByRole('treeitem', { name: /^docs/ }).getAttribute('aria-expanded')).toBe('true')
    const guide = within(tree()).getByRole('treeitem', { name: /^guide-sess-a\.md/ })
    expect(body().scrollTop).toBe(400)
    expect(explorer().querySelector('.initial-skeleton')).toBeNull()
    expect(within(panel()).getByText('None yet: this session asks you each time.')).toBeTruthy()
    expect(within(panel()).queryByText('Loading permissions…')).toBeNull()
    expect((within(panel()).getByRole('checkbox', { name: 'Nudge when the plan drifts' }) as HTMLInputElement).checked).toBe(true)
    if (shotDir) await page.screenshot({ path: `${shotDir}/switch-back-${width}x${height}.png` })
    // Focus a row: the reads that land must not move it, nor replace the row.
    guide.focus({ preventScroll: true })
    await release()
    await settle()
    expect(document.activeElement).toBe(guide)
    expect(guide.isConnected).toBe(true)
    expect(body().scrollTop).toBe(400)
    expect(within(tree()).getByRole('treeitem', { name: /^docs/ }).getAttribute('aria-expanded')).toBe('true')
    // Only the visible listings (the root, the open folder), one at a time, and the open tab's reads.
    expect(workspaceCalls()).toEqual(['list:', 'list:docs'])
    expect(server.workspaceMaxInFlight).toBe(1)
    expect(server.calls.filter(u => u.endsWith('/sess-a/permissions')).length).toBeLessThanOrEqual(2)
    expect(server.calls.filter(u => u.endsWith('/plan-nudges'))).toEqual(['/api/sessions/sess-a/plan-nudges'])
    expect(server.calls.some(u => u.endsWith('/topics') || u.endsWith('/capabilities'))).toBe(false)
  })
}

it('a closed folder is not read on return; opening it shows its cached listing and reads it again quietly', async () => {
  await page.viewport(1440, 900)
  mount()
  await visitA()
  await userEvent.click(within(tree()).getByRole('treeitem', { name: /^docs/ }))
  await settle()
  await toB()
  server.calls.length = 0
  await act(async () => go('/sessions/sess-a'))
  await settle()
  expect(workspaceCalls()).toEqual(['list:'])
  server.hold = true
  await userEvent.click(within(tree()).getByRole('treeitem', { name: /^docs/ }))
  expect(within(tree()).getByRole('treeitem', { name: /^guide-sess-a\.md/ })).toBeTruthy()
  expect(explorer().querySelector('.initial-skeleton')).toBeNull()
  await release()
  await settle()
  expect(workspaceCalls()).toEqual(['list:', 'list:docs'])
})

it('without the cache the same return shows the initial skeleton and waits for the reads', async () => {
  await page.viewport(1440, 900)
  mount()
  await visitA()
  await toB()
  // A cache that was dropped is the old behavior: every part waits for its read.
  useSessionStore.setState(s => ({ sessions: s.sessions.filter(x => x.id !== 'sess-a') }))
  useSessionStore.setState(s => ({ sessions: [...s.sessions, makeSession({ id: 'sess-a', kind: 'agent', status: 'idle', title: 'Session A' })] }))
  expect(cachedViewSessions()).toEqual(['sess-b'])
  server.hold = true
  await act(async () => go('/sessions/sess-a'))
  expect(within(explorer()).queryByRole('treeitem', { name: /^README-sess-a\.md/ })).toBeNull()
  expect(await within(explorer()).findByText('Loading workspace')).toBeTruthy()
  expect(within(explorer()).getByRole('region', { name: 'workspace' }).getAttribute('aria-busy')).toBe('true')
  expect(within(panel()).getByText('Loading permissions…')).toBeTruthy()
  await release()
})

it('the History tab comes back at once and is asked again with its ETag', async () => {
  localStorage.setItem(RAIL_TAB_KEY, 'history')
  await page.viewport(1440, 900)
  mount()
  await within(panel()).findByText('Cache the explorer for sess-a')
  await toB()
  await within(panel()).findByText('Cache the explorer for sess-b')
  server.calls.length = 0
  server.hold = true
  await act(async () => go('/sessions/sess-a'))
  expect(within(panel()).getByText('Cache the explorer for sess-a')).toBeTruthy()
  expect(within(panel()).queryByTestId('reel-loading')).toBeNull()
  await release()
  await settle()
  expect(server.calls.filter(u => u.endsWith('/topics'))).toEqual(['/api/sessions/sess-a/topics'])
  expect(server.calls.some(u => u.endsWith('/plan-nudges'))).toBe(false)
})

it('a failed background read keeps the cached view and says so', async () => {
  localStorage.setItem(RAIL_TAB_KEY, 'history')
  await page.viewport(1440, 900)
  mount()
  await within(explorer()).findByRole('treeitem', { name: /^README-sess-a\.md/ })
  await within(panel()).findByText('Cache the explorer for sess-a')
  await toB()
  server.fail = new Set(['/sess-a/workspace', '/sess-a/topics'])
  await act(async () => go('/sessions/sess-a'))
  await settle()
  expect(within(tree()).getByRole('treeitem', { name: /^README-sess-a\.md/ })).toBeTruthy()
  expect(within(explorer()).getByRole('status').textContent).toBe('The daemon is not answering.')
  expect(explorer().querySelector('.initial-skeleton')).toBeNull()
  expect(within(panel()).getByText('Cache the explorer for sess-a')).toBeTruthy()
  expect(await within(panel()).findByTestId('reel-stale')).toBeTruthy()
})

it('a runtime change discards the cached pages instead of mixing them in', async () => {
  await page.viewport(1440, 900)
  mount()
  await visitA()
  await toB()
  server.generation['sess-a'] = 2
  server.calls.length = 0
  await act(async () => go('/sessions/sess-a'))
  await within(tree()).findByRole('treeitem', { name: /^FRESH\.md/ })
  await settle()
  // Read afresh from the root: the open folder of the runtime before is closed, its page gone.
  expect(within(tree()).queryByRole('treeitem', { name: /^README-sess-a\.md/ })).toBeNull()
  expect(within(tree()).getByRole('treeitem', { name: /^docs/ }).getAttribute('aria-expanded')).toBe('false')
  expect(within(tree()).queryByRole('treeitem', { name: /^guide-sess-a\.md/ })).toBeNull()
  const snapshot = readView<ExplorerSnapshot>('person-a', 'sess-a', 'explorer')!
  expect(snapshot.binding).toBe('launch:2')
  expect(Object.keys(snapshot.pages)).toEqual([''])
  expect(snapshot.expanded).toEqual([''])
})

it('Refresh still reads everything again', async () => {
  await page.viewport(1440, 900)
  mount()
  await visitA()
  await toB()
  await act(async () => go('/sessions/sess-a'))
  await settle()
  server.calls.length = 0
  server.hold = true
  await userEvent.click(within(explorer()).getByRole('button', { name: 'Refresh workspace' }))
  expect(explorer().textContent).toContain('Refreshing workspace…')
  expect(within(tree()).queryByRole('treeitem', { name: /^guide-sess-a\.md/ })).toBeNull()
  await release()
  await within(explorer()).findByText('Workspace refreshed.')
  expect(workspaceCalls()).toEqual(['list:'])
})

it('another account sees none of it', async () => {
  await page.viewport(1440, 900)
  mount()
  await visitA()
  await toB()
  who.subject = 'person-b'
  server.hold = true
  await act(async () => go('/sessions/sess-a'))
  expect(within(explorer()).queryByRole('treeitem', { name: /^README-sess-a\.md/ })).toBeNull()
  expect(within(panel()).queryByText('None yet: this session asks you each time.')).toBeNull()
  expect(readView('person-a', 'sess-b', 'explorer')).toBeUndefined()
  await release()
})

it('an ended, archived or discarded session is dropped', async () => {
  useSessionStore.setState(s => ({ sessions: [...s.sessions, makeSession({ id: 'sess-c', kind: 'agent', status: 'idle', title: 'Session C' })] }))
  await page.viewport(1440, 900)
  mount()
  await visitA()
  await toB()
  await act(async () => go('/sessions/sess-c'))
  await within(explorer()).findByRole('treeitem', { name: /^README-sess-c\.md/ })
  await settle()
  expect(cachedViewSessions().sort()).toEqual(['sess-a', 'sess-b', 'sess-c'])
  act(() => useSessionStore.getState().updateSessionStatus('sess-a', 'ended'))
  expect(cachedViewSessions().sort()).toEqual(['sess-b', 'sess-c'])
  act(() => useSessionStore.setState(s => ({ sessions: s.sessions.map(x => x.id === 'sess-b' ? { ...x, archived_at: '2026-10-27T12:00:00Z' } : x) })))
  expect(cachedViewSessions()).toEqual(['sess-c'])
  act(() => useSessionStore.setState(s => ({ sessions: s.sessions.filter(x => x.id !== 'sess-c') })))
  expect(cachedViewSessions()).toEqual([])
})

it('on a phone the explorer sheet opens on the cached tree at once', async () => {
  await page.viewport(390, 844)
  mount()
  const openSheet = async () => { await userEvent.click(screen.getByRole('button', { name: 'Toggle explorer' })); return screen.findByRole('dialog', { name: 'Explorer' }) }
  let sheet = screen.queryByRole('dialog', { name: 'Explorer' }) ?? await openSheet()
  await within(sheet).findByRole('treeitem', { name: /^README-sess-a\.md/ })
  await userEvent.click(within(sheet).getByRole('treeitem', { name: /^docs/ }))
  await within(sheet).findByRole('treeitem', { name: /^guide-sess-a\.md/ })
  await settle()
  await act(async () => go('/sessions/sess-b'))
  await settle()
  server.calls.length = 0
  server.hold = true
  await act(async () => go('/sessions/sess-a'))
  sheet = screen.queryByRole('dialog', { name: 'Explorer' }) ?? await openSheet()
  expect(within(sheet).getByRole('treeitem', { name: /^guide-sess-a\.md/ })).toBeTruthy()
  expect(sheet.querySelector('.initial-skeleton')).toBeNull()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  if (shotDir) await page.screenshot({ path: `${shotDir}/switch-back-390x844.png` })
  await release()
  await settle()
  expect(workspaceCalls()).toEqual(['list:', 'list:docs'])
})
