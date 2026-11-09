import App from '../App'
import { clearBoardPageCache } from '../lib/boardPageCache'
import { resetLiveBoardCardsForTest } from '../lib/liveBoardCard'
import { resetBoardKeyMapsForTest } from '../lib/cardRefs'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { commands, page, userEvent } from 'vitest/browser'
import { useTranscriptStore } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { invalidateChatTransport } from '../lib/chatTransport'
import { resetSessionBoardLinksForTest } from '../lib/sessionBoardLinks'
import { resetBoardOriginForTest } from '../lib/boardLink'
import type { BoardAccess } from '../lib/sessionPermissions'
import '../index.css'
import '@blerglab/chat/blerg.css'

vi.mock('../shellTransport', async original => {
  const actual = await original<typeof import('../shellTransport')>()
  const { FakeShell } = await import('../test/shellMock')
  return { ...actual, openShell: vi.fn(async () => new FakeShell('tab-shell')), restoreShells: vi.fn(async () => []) }
})
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
let boards: BoardAccess[] = []
let cardColumn = 'todo'
beforeEach(() => {
 clearBoardPageCache()
  localStorage.clear()
  cardColumn = 'todo'
  resetLiveBoardCardsForTest()
  resetBoardKeyMapsForTest()
  resetSessionBoardLinksForTest()
  resetBoardOriginForTest()
  document.documentElement.setAttribute('data-theme', 'dark')
  useTranscriptStore.setState({ sessions: {} })
  useTranscriptStore.getState().ingestReplayDone('sess-a', { lastSeq: 0, hasMore: false })
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ kind: 'agent', runtime: 'daemon', status: 'idle', title: 'Plan the release' })] })
  apiFetch.mockReset().mockImplementation(async (url: string) => {
    if (url.includes('/board-cards/b-rel/view')) return json({ columns: [{ id: 'todo', name: 'To do' }, { id: 'done', name: 'Done' }], cards: [{ id: 'card-1', board_id: 'b-rel', number: 1, reference: 'REL-1', title: 'Ship the release', tags: ['to-test'], version: 4, rank: 'a', column_id: cardColumn }], releases: [{ id: 'r1', name: '0.6.0', status: 'in progress' }] })
    if (url.includes('/board-cards/b-rel/1/move')) { cardColumn = 'done'; return json({ moved: true }) }
    if (url.includes('/board-cards/b-rel/1')) return json({ card: { id: 'card-1', board_id: 'b-rel', number: 1, title: 'Ship the release', version: 4, rank: 'a', column_id: cardColumn, columns: [{ id: 'todo', name: 'To do' }, { id: 'done', name: 'Done' }] } })
    if (url.startsWith('/api/models/')) return json({ source: 'live', models: [{ id: 'claude-sonnet-4-6', name: 'Sonnet', section: 'main', efforts: ['low', 'high'], default_effort: 'high' }] })
    if (url.includes('shell-capability')) return json({ available: true })
    if (url.includes('/live')) return json({ session_id: 'sess-a', through_seq: 0, generated_at: new Date().toISOString(), tokens_hidden: true, asks: [], subagents: [], tasks: [], tool_changes: 0, counts: { needs_you: 0, running: 0 }, can_release_prompt: true, children: [{ id: 'worker-a', title: 'Release worker', engine: 'codex', model: '', status: 'running', started_at: '2026-10-27T00:00:00Z', asking: false, tokens: null, ticket: { source: 'blerg-board', board_id: 'b-rel', key: '#1', title: 'Ship the release', status: 'To do' } }] })
    if (url.endsWith('/permissions')) return json({ can_grant: true, reason: '', grants: [], mcp_tools: [], boards })
    if (url === '/api/site-board') return json({ board_url: 'https://board.example.com' })
    if (url === '/api/session-boards') return json({ configured: true, boards: [{ id: 'b-rel', key: 'REL', name: boards.find(b => b.board_id === 'b-rel')?.board_name ?? 'Release planning' }] })
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn(async () => json([])))
})
afterEach(() => { window.history.replaceState({}, '', '/'); invalidateChatTransport(); vi.unstubAllGlobals(); document.documentElement.removeAttribute('data-theme') })

function mount() {
  return render(<MemoryRouter initialEntries={['/sessions/sess-a']}><div style={{ height: '100vh' }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div></MemoryRouter>)
}
// Shell, board, Insights and blackboard moved from the tab bar into the session header's "opens a
// tab" group (card BLG-450); below a narrow width they collapse into one Open menu instead.
const bar = () => screen.getByTestId('session-header-controls')
async function openOpenMenu() {
  await userEvent.click(await screen.findByTestId('open-tab-menu'))
}
function inside(el: Element) {
  const r = el.getBoundingClientRect()
  expect(r.width).toBeGreaterThan(0)
  expect(r.left).toBeGreaterThanOrEqual(0)
  expect(r.right).toBeLessThanOrEqual(innerWidth + 1)
  expect(r.bottom).toBeLessThanOrEqual(innerHeight + 1)
}

for (const [width, height] of [[390, 844], [3840, 2160]]) {
  it(`shows no board button for a session without a board at ${width}`, async () => {
    await page.viewport(width, height)
    boards = [{ board_id: 'b-x', board_name: 'Proposals only', level: 'propose' }]
    mount()
    await waitFor(() => expect(apiFetch.mock.calls.some(([u]) => String(u).endsWith('/sess-a/permissions'))).toBe(true))
    await waitFor(() => expect(within(bar()).getByTestId('rail-toggle')).toBeTruthy())
    expect(within(bar()).queryByTestId('session-board-button')).toBeNull()
  })

  // Card BLG-450: shell, board, Insights and blackboard share the header's "opens a tab" group, in
  // that order — a phone (390) collapses the whole group into one Open menu first.
  it(`puts one board's opener next to Shell and Insights at ${width}`, async () => {
    await page.viewport(width, height)
    boards = [{ board_id: 'b-rel', level: 'full', launch: true }]
    mount()
    if (width === 390) await openOpenMenu()
    const role = width === 390 ? 'menuitem' : 'button'
    // Inside the collapsed Open menu, the board opener is a role="menuitem" anchor (still a real
    // link: same href/target/rel/testid as the expanded form), not role="link".
    const link = await within(bar()).findByRole(width === 390 ? 'menuitem' : 'link', { name: 'Open board: Release planning' })
    await waitFor(() => expect(link.getAttribute('href')).toBe('https://board.example.com/boards/b-rel'))
    expect(link.getAttribute('target')).toBe('_blank')
    expect(link.getAttribute('rel')).toBe('noopener')
    const shell = await within(bar()).findByRole(role, { name: 'New shell' })
    const insights = within(bar()).getByRole(role, { name: 'Insights' })
    expect(link.previousElementSibling).toBe(shell)
    expect(link.nextElementSibling).toBe(insights)
    if (width !== 390) inside(link)
  })

  it(`opens a popover of several boards at ${width}`, async () => {
    await page.viewport(width, height)
    boards = [{ board_id: 'b-ops', board_name: 'Ops', level: 'read' }, { board_id: 'b-rel', level: 'full', launch: true }]
    mount()
    if (width === 390) {
      // Several boards list directly as rows in the collapsed Open menu, instead of opening a
      // second, nested popover.
      await openOpenMenu()
      const menu = within(screen.getByRole('menu', { name: 'Open' }))
      const items = await menu.findAllByRole('menuitem', { name: /^Open board:/ })
      expect(items.map(i => i.getAttribute('href'))).toEqual(['https://board.example.com/boards/b-rel', 'https://board.example.com/boards/b-ops'])
      return
    }
    const button = await within(bar()).findByRole('button', { name: 'Open a board' })
    await waitFor(() => expect(apiFetch.mock.calls.some(([u]) => u === '/api/site-board')).toBe(true))
    await new Promise(r => setTimeout(r, 0))
    await userEvent.click(button)
    const menu = await screen.findByRole('menu', { name: 'Open a board' })
    const items = within(menu).getAllByRole('menuitem')
    expect(items.map(i => i.getAttribute('href'))).toEqual(['https://board.example.com/boards/b-rel', 'https://board.example.com/boards/b-ops'])
    expect(items[0].textContent).toContain('Release planning')
    await waitFor(() => expect(document.activeElement).toBe(items[0]))
    const card = screen.getByTestId('session-board-menu')
    await waitFor(() => expect(card.getAnimations().length).toBe(0))
    inside(card)
    for (const item of items) inside(item)
    expect(card.getBoundingClientRect().top).toBeGreaterThanOrEqual(button.getBoundingClientRect().bottom)
    await userEvent.keyboard('{ArrowDown}')
    expect(document.activeElement).toBe(items[1])
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('menu')).toBeNull())
    expect(document.activeElement).toBe(button)
  })
}

it('keeps the button for an archived session', async () => {
  await page.viewport(1440, 900)
  useSessionStore.setState({ sessions: [makeSession({ kind: 'agent', status: 'stopped', archived_at: '2026-10-26T00:00:00Z', title: 'Plan the release' })] })
  boards = [{ board_id: 'b-ops', board_name: 'Ops', level: 'read' }]
  mount()
  expect(await within(bar()).findByRole('link', { name: 'Open board: Ops' })).toBeTruthy()
})

for (const width of [390, 3840]) it(`opens a native board preview at ${width}`, async () => {
  await page.viewport(width, 900)
  boards = [{ board_id: 'b-rel', board_name: 'Release planning', level: 'full' }]
  mount()
  if (width === 390) await openOpenMenu()
  await userEvent.click(await screen.findByTestId('session-board-button'))
  await waitFor(() => expect(document.querySelector('[data-view-id^="board:b-rel:"]')).toBeTruthy())
  const columns = await screen.findByRole('region', { name: 'Board columns' })
  await waitFor(() => inside(columns))
  await waitFor(() => expect(document.activeElement).toBe(columns))
  const card = await screen.findByRole('button', { name: /REL-1.*Ship the release/ })
  await waitFor(() => expect(screen.getByRole('button', { name: 'Release worker, running' })).toBeVisible())
  inside(card)
  if (width === 3840) expect(screen.getByRole('region', { name: 'To do' }).getBoundingClientRect().width).toBeLessThanOrEqual(290)
  if (width === 390) expect(screen.getByText('Tap a card, then Status to move it.')).toBeVisible()
  expect(card.draggable).toBe(true)
  const transfer = new DataTransfer()
  act(() => { card.dispatchEvent(new DragEvent('dragstart', { bubbles: true, cancelable: true, dataTransfer: transfer })) })
  expect(transfer.getData('application/x-blerg-board-card')).toBe(JSON.stringify({ board: 'b-rel', number: 1, version: 4, column: 'todo' }))
  act(() => { within(columns).getByRole('region', { name: 'To do' }).dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: transfer })) })
  expect(apiFetch.mock.calls.some(([u]) => String(u).includes('/1/move'))).toBe(false)
  act(() => { within(columns).getByRole('region', { name: 'Done' }).dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: transfer })) })
  await waitFor(() => expect(apiFetch.mock.calls.some(([u, init]) => String(u).includes('/1/move') && init?.body === JSON.stringify({ column_id: 'done', if_match: 4 }))).toBe(true))
  await waitFor(() => expect(within(within(columns).getByRole('region', { name: 'Done' })).getByRole('button', { name: /REL-1.*Ship the release/ })).toBeTruthy())
  await userEvent.click(within(within(columns).getByRole('region', { name: 'Done' })).getByRole('button', { name: /REL-1.*Ship the release/ }))
  expect(await screen.findByTestId('board-card-live')).toBeTruthy()
  expect(screen.getByRole('dialog')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Open as tab' })).toBeVisible()
  await userEvent.keyboard('{Escape}')
  await page.getByRole('button', { name: 'Filter', exact: true }).click()
  await page.getByRole('button', { name: 'Tag to-test, 1 card' }).click()
  await userEvent.keyboard('{Escape}')
  expect(screen.getByRole('button', { name: 'Remove to-test' })).toBeTruthy()
  fireEvent.change(screen.getByRole('combobox', { name: 'Filter board release' }), { target: { value: 'r1' } })
  await waitFor(() => expect(apiFetch.mock.calls.some(([u]) => String(u).includes('release=r1'))).toBe(true))
  expect(document.querySelector('iframe')).toBeNull()
  await page.getByRole('button', { name: /REL-1.*Ship the release/ }).click()
  expect(await screen.findByTestId('board-card-live')).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Open board tab' })).toBeNull()
  await userEvent.keyboard('{Escape}')
  await waitFor(() => expect(screen.queryByTestId('board-card-live')).toBeNull())
  expect(document.querySelectorAll('[data-view-id^="board:b-rel:"]')).toHaveLength(1)
  if (width === 3840) {
    const tab = screen.getByRole('tab', { name: 'Release planning' }).parentElement!
    const tabTransfer = new DataTransfer()
    act(() => { tab.dispatchEvent(new DragEvent('dragstart', { bubbles: true, cancelable: true, dataTransfer: tabTransfer })) })
    const group = document.querySelector<HTMLElement>('[data-group-id]')!
    const bounds = group.getBoundingClientRect()
    const init = { bubbles: true, cancelable: true, dataTransfer: tabTransfer, clientX: bounds.right - 4, clientY: bounds.top + bounds.height / 2 }
    act(() => { group.dispatchEvent(new DragEvent('dragover', init)); group.dispatchEvent(new DragEvent('drop', init)) })
    await waitFor(() => expect(document.querySelectorAll('[data-group-id]')).toHaveLength(2))
    inside(screen.getByRole('region', { name: 'Board columns' }))
    expect(document.querySelector('.session-tab.preview')).toBeNull()
  }
})

for (const width of [390, 3840]) it(`keeps Read board access read-only at ${width}`, async () => {
 await page.viewport(width, 900)
 boards = [{ board_id: 'b-rel', board_name: 'Release planning', level: 'read' }]
 mount()
 if (width === 390) await openOpenMenu()
 await userEvent.click(await screen.findByTestId('session-board-button'))
 const card = await screen.findByRole('button', { name: /REL-1.*Ship the release/ })
 expect(card.draggable).toBe(false)
 await userEvent.click(card)
 const picker = await screen.findByTestId('board-card-columns')
 expect(within(picker).getAllByRole('button').every(button => (button as HTMLButtonElement).disabled)).toBe(true)
 expect(apiFetch.mock.calls.some(([u]) => String(u).includes('/move'))).toBe(false)
 await userEvent.click(screen.getByRole('button', { name: 'Open as tab' }))
 const tabPicker = await screen.findByTestId('board-card-columns')
 expect(within(tabPicker).getAllByRole('button').every(button => (button as HTMLButtonElement).disabled)).toBe(true)
 await userEvent.click(screen.getByRole('tab', { name: 'Release planning' }))
 boards = [{ board_id: 'b-rel', board_name: 'Release planning', level: 'propose' }]
 await page.getByRole('button', { name: 'Refresh', exact: true }).click()
 expect(await within(screen.getByRole('region', { name: 'Release planning' })).findByRole('status')).toHaveTextContent('This workspace cannot read this board.')
 expect(screen.getByRole('button', { name: /REL-1.*Ship the release/ }).draggable).toBe(false)
})

for (const width of [390, 3840]) it(`opens one correctly named board tab from real timeline keys at ${width}`, async () => {
 await page.viewport(width, 900)
 boards = [{ board_id: 'b-rel', board_name: 'Release planning', level: 'read' }]
 const store = useTranscriptStore.getState()
 store.ingest({ type: 'agent_event', session_id: 'sess-a', client_event_id: 'text-1', seq: 1, ts: '2026-10-27T00:00:00Z', kind: 'assistant_text', payload: { text: 'See REL-1 and board:b-rel#1.', done: true } })
 store.ingestReplayDone('sess-a', { lastSeq: 1, hasMore: false })
 mount()
 const timeline = await waitFor(() => { const el = document.querySelector<HTMLElement>('.agent-timeline'); expect(el).toBeTruthy(); return el! })
 const chips = await within(timeline).findAllByTestId('md-ref')
 expect(chips).toHaveLength(2)
 await userEvent.click(chips[0])
 await screen.findByTestId('board-card-live')
 await userEvent.click(await screen.findByRole('button', { name: /^Open board tab$/ }))
 await screen.findByRole('tab', { name: 'Release planning' })
 await userEvent.click(screen.getByRole('tab', { name: /Main/ }))
 chips[1].focus()
 await userEvent.keyboard('{Enter}')
 await screen.findByTestId('board-card-live')
 await userEvent.click(await screen.findByRole('button', { name: /^Open board tab$/ }))
 expect(document.querySelectorAll('[data-view-id^="board:b-rel:"]')).toHaveLength(2)
 const original = apiFetch.getMockImplementation()!
 apiFetch.mockImplementation(async (url: string, init?: RequestInit) => url === '/api/session-boards' ? json({ boards: [{ id: 'b-rel', key: 'REL', name: 'Renamed release board' }] }) : original(url, init))
 await userEvent.click(screen.getByRole('button', { name: /^Refresh$/ }))
 await screen.findByRole('tab', { name: 'Renamed release board' })
})

it('shows a stale drag conflict and keeps the last card data', async () => {
 await page.viewport(390, 844)
 boards = [{ board_id: 'b-rel', board_name: 'Release planning', level: 'full' }]
 const original = apiFetch.getMockImplementation()!
 apiFetch.mockImplementation(async (url: string, init?: RequestInit) => url.includes('/1/move') ? new Response(JSON.stringify({ error: 'The card changed. Refresh before moving it.' }), { status: 409 }) : original(url, init))
 mount()
 await openOpenMenu()
 await userEvent.click(await screen.findByTestId('session-board-button'))
 const card = await screen.findByRole('button', { name: /REL-1.*Ship the release/ })
 const transfer = new DataTransfer()
 act(() => {
  card.dispatchEvent(new DragEvent('dragstart', { bubbles: true, cancelable: true, dataTransfer: transfer }))
  screen.getByRole('region', { name: 'Done' }).dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: transfer }))
 })
 expect(await screen.findByText(/The card changed\./)).toBeTruthy()
 expect(within(screen.getByRole('region', { name: 'To do' })).getByRole('button', { name: /REL-1/ })).toBeTruthy()
 const move = apiFetch.mock.calls.find(([u]) => String(u).includes('/1/move'))!
 expect(move[0]).toContain('tab=1')
 expect(JSON.parse(move[1].body)).toEqual({ column_id: 'done', if_match: 4 })
})

function mountWorkspaceApp() { window.history.replaceState({}, '', '/sessions/sess-a'); return render(<App />) }

it.each([390, 1440, 3840])('focuses and duplicates a dense board through the workspace controls at %s', async width => {
 const { denseCards, parityColumns } = await import('../../../../packages/board/parityFixture')
 await page.viewport(width, width === 390 ? 844 : 2160)
 boards = [{ board_id: 'b-rel', board_name: 'Release planning', level: 'full', launch: true }]
 const original = apiFetch.getMockImplementation()!
 apiFetch.mockImplementation(async (url: string) => url.includes('/board-cards/b-rel/view') ? json({ columns: parityColumns, cards: denseCards.map(c => ({ ...c, board_id: 'b-rel' })), releases: [] }) : original(url))
 mountWorkspaceApp()
 if (width === 390) await openOpenMenu()
 await userEvent.click(await screen.findByRole(width === 390 ? 'menuitem' : 'link', { name: 'Open board: Release planning' }))
 await screen.findByRole('button', { name: 'Focus In progress' })
 const shots = import.meta.env.VITE_BOARD_SHOTS_DIR
 if (shots) await page.screenshot({ path: `${shots}/workspace-before-focus-${width}.png` })
 fireEvent.doubleClick(screen.getByRole('heading', { name: 'In progress' }))
 await waitFor(() => expect(document.querySelectorAll('.shared-board-column')).toHaveLength(1))
 expect(document.querySelectorAll('.shared-board-card')).toHaveLength(80)
 if (shots) await page.screenshot({ path: `${shots}/workspace-focus-${width}.png` })
 const tab = screen.getByRole('tab', { name: /Release planning.*In progress/ })
 fireEvent.contextMenu(tab, { clientX: 250, clientY: 100 })
 expect(screen.getByRole('menu', { name: 'Tab actions' })).toBeTruthy()
 if (shots) await page.screenshot({ path: `${shots}/workspace-tab-menu-${width}.png` })
 fireEvent.click(screen.getByRole('menuitem', { name: 'Duplicate' }))
 await waitFor(() => expect(screen.getAllByRole('tab', { name: /Release planning/ })).toHaveLength(2))
 expect(screen.getAllByRole('button', { name: 'Back to all columns' }).length).toBeGreaterThan(0)
 if (width === 3840) {
  const copy = screen.getAllByRole('tab', { name: /Release planning/ }).at(-1)!
  fireEvent.keyDown(copy, { key: 'ContextMenu' })
  fireEvent.click(screen.getByRole('menuitem', { name: 'Move to a split panel' }))
  await waitFor(() => expect(document.querySelectorAll('.session-board:not([hidden])').length).toBe(2))
  if (shots) await page.screenshot({ path: `${shots}/workspace-split-boards-${width}.png` })
 }
})

it.each([390, 3840])('keeps the real board geometry while the first load is pending at %s', async width => {
 await page.viewport(width, width === 390 ? 844 : 2160)
 boards = [{ board_id: 'b-rel', board_name: 'Release planning', level: 'full', launch: true }]
 const original = apiFetch.getMockImplementation()!
 let finish!: (response: Response) => void
 const pending = new Promise<Response>(resolve => { finish = resolve })
 apiFetch.mockImplementation(async (url: string) => url.includes('/board-cards/b-rel/view') ? pending : original(url))
 mountWorkspaceApp()
 if (width === 390) await openOpenMenu()
 await userEvent.click(await screen.findByRole(width === 390 ? 'menuitem' : 'link', { name: 'Open board: Release planning' }))
 await screen.findByText('Loading board…')
 await waitFor(() => expect(document.querySelectorAll('.shared-board-skeleton')).toHaveLength(4))
 const before = document.querySelector('.shared-board-skeleton')!.getBoundingClientRect()
 expect(before.width).toBe(290)
 const shots = import.meta.env.VITE_BOARD_SHOTS_DIR
 if (shots) await page.screenshot({ path: `${shots}/workspace-loading-${width}.png` })
 await commands.setUsageReducedMotion(true)
 try { expect(getComputedStyle(document.querySelector('.shared-board-skeleton .initial-skeleton-value')!, '::after').animationName).toBe('none') }
 finally { await commands.setUsageReducedMotion(false) }
 finish(await original('/api/board-cards/b-rel/view'))
 await screen.findByRole('button', { name: /REL-1.*Ship the release/ })
 expect(document.querySelector('.shared-board-skeleton')).toBeNull()
 const after = document.querySelector('.shared-board-column')!.getBoundingClientRect()
 expect(after.left).toBe(before.left)
 expect(after.width).toBe(before.width)
})

it.each([390, 3840])('keeps the loaded board with a quiet retry notice after refresh fails at %s', async width => {
 await page.viewport(width, width === 390 ? 844 : 2160)
 boards = [{ board_id: 'b-rel', board_name: 'Release planning', level: 'full', launch: true }]
 mountWorkspaceApp()
 if (width === 390) await openOpenMenu()
 await userEvent.click(await screen.findByRole(width === 390 ? 'menuitem' : 'link', { name: 'Open board: Release planning' }))
 await screen.findByRole('button', { name: /REL-1.*Ship the release/ })
 const shots = import.meta.env.VITE_BOARD_SHOTS_DIR
 if (shots) await page.screenshot({ path: `${shots}/workspace-before-refresh-${width}.png` })
 const original = apiFetch.getMockImplementation()!
 apiFetch.mockImplementation(async (url: string) => url.includes('/view?') ? new Response(JSON.stringify({ error: 'Board is busy. Try again shortly.' }), { status: 429 }) : original(url))
 fireEvent.click(screen.getByRole('button', { name: /^Refresh$/ }))
 await screen.findByText(/Showing what was loaded at/)
 expect(screen.getByRole('button', { name: /REL-1.*Ship the release/ })).toBeTruthy()
 if (shots) await page.screenshot({ path: `${shots}/workspace-refresh-notice-${width}.png` })
 fireEvent.click(screen.getByRole('button', { name: 'Dismiss board notice' }))
 expect(screen.queryByText(/Showing what was loaded at/)).toBeNull()
 apiFetch.mockImplementation(original)
})

it.each([390, 3840])('restores loaded cards immediately when returning to the workspace at %s', async width => {
 await page.viewport(width, width === 390 ? 844 : 2160)
 boards = [{ board_id: 'b-rel', board_name: 'Release planning', level: 'full', launch: true }]
 const view = mountWorkspaceApp()
 if (width === 390) await openOpenMenu()
 await userEvent.click(await screen.findByRole(width === 390 ? 'menuitem' : 'link', { name: 'Open board: Release planning' }))
 await screen.findByRole('button', { name: /REL-1.*Ship the release/ })
 view.unmount()
 const original = apiFetch.getMockImplementation()!
 apiFetch.mockImplementation(async (url: string) => url.includes('/view?') ? new Promise<Response>(() => {}) : original(url))
 mountWorkspaceApp()
 expect(screen.getByRole('button', { name: /REL-1.*Ship the release/ })).toBeTruthy()
 expect(document.querySelector('.shared-board-skeleton')).toBeNull()
 const shots = import.meta.env.VITE_BOARD_SHOTS_DIR
 if (shots) await page.screenshot({ path: `${shots}/workspace-cached-board-${width}.png` })
})

it.each([390, 3840])('uses plain shared menu rows for shell actions without an ellipsis at %s', async width => {
 await page.viewport(width, width === 390 ? 844 : 2160)
 mountWorkspaceApp()
 if (width === 390) await openOpenMenu()
 await userEvent.click(await screen.findByRole(width === 390 ? 'menuitem' : 'button', { name: 'New shell' }))
 const tab = await screen.findByRole('tab', { name: 'Shell' })
 expect(document.querySelector('.shell-tab-menu')).toBeNull()
 expect(screen.queryByRole('button', { name: /Shell actions for/ })).toBeNull()
 if (width === 390) {
  fireEvent.touchStart(tab, { touches: [new Touch({ identifier: 1, target: tab, clientX: 160, clientY: 100 })] })
  await screen.findByRole('menu', { name: 'Tab actions' })
  fireEvent.touchEnd(tab)
 } else fireEvent.keyDown(tab, { key: 'ContextMenu' })
 const rename = screen.getByRole('menuitem', { name: 'Rename' })
 expect(rename.closest('.dock-row-menu')).toBeTruthy()
 expect(screen.queryByRole('menuitem', { name: 'Duplicate' })).toBeNull()
 const shots = import.meta.env.VITE_BOARD_SHOTS_DIR
 if (shots) await page.screenshot({ path: `${shots}/workspace-shell-menu-${width}.png` })
 fireEvent.click(rename)
 await screen.findByRole('textbox', { name: 'Shell name' })
})

it('closes the shared dialog before switching boards and keeps column focus independent', async () => {
 await page.viewport(1440, 900)
 boards = [{ board_id: 'b-rel', board_name: 'Release planning', level: 'full', launch: true }]
 mountWorkspaceApp()
 await userEvent.click(await screen.findByRole('link', { name: 'Open board: Release planning' }))
 await userEvent.click(await screen.findByRole('button', { name: /REL-1.*Ship the release/ }))
 await screen.findByTestId('board-card-live')
 await userEvent.keyboard('{Escape}')
 expect(screen.queryByRole('dialog')).toBeNull()
 expect(screen.getByRole('tab', { name: 'Release planning' })).toBeVisible()
 await userEvent.click(screen.getByRole('link', { name: 'Open board: Release planning' }))
 await userEvent.click(await screen.findByRole('button', { name: 'Focus To do' }))
 fireEvent.keyDown(document, { key: 'Escape' })
 expect(screen.queryByRole('button', { name: 'Back to all columns' })).toBeNull()
 await userEvent.click(screen.getByRole('tab', { name: /^Release planning$/ }))
 expect(screen.queryByRole('dialog')).toBeNull()
 expect(screen.queryByRole('button', { name: 'Back to all columns' })).toBeNull()
})

it.each([
 { width: 390, pane: 390, theme: 'dark' as const },
 { width: 3840, pane: 1024, theme: 'dark' as const },
 { width: 3840, pane: 3840, theme: 'dark' as const },
 { width: 3840, pane: 1024, theme: 'colorblind-dark' as const },
])('BLG-505: worker glyphs, live dialog and ticket tab at $width, pane $pane, $theme', async ({ width, pane, theme }) => {
 const { setTheme } = await import('../../../../packages/settings/theme')
 await page.viewport(width, width === 390 ? 844 : 2160)
 setTheme(theme)
 boards = [{ board_id: 'b-rel', board_name: 'Release planning', level: 'full', launch: true }]
 const cards = ['Ship the release', 'Review the migration', 'Write release notes'].map((title, i) => ({ id: `card-${i + 1}`, board_id: 'b-rel', number: i + 1, reference: `REL-${i + 1}`, title, version: 4, rank: String(i), column_id: 'todo', tags: ['0.6.0'], body: 'Keep the release focused and record the verification results.' }))
 const workers = [
  { id: 'worker-a', title: 'Release worker', status: 'running', worker_kind: 'card' as const, number: 1 },
  { id: 'worker-b', title: 'Migration reviewer', status: 'idle', worker_kind: 'review' as const, number: 2 },
  { id: 'worker-c', title: 'Migration research', status: 'waiting', worker_kind: 'research' as const, number: 2 },
 ]
 useSessionStore.setState(s => ({ sessions: [...s.sessions, ...workers.map(w => makeSession({ ...w, kind: 'agent', parent_session_id: 'sess-a', started_by: { kind: 'session' }, runtime: 'daemon', status: w.status as 'running' | 'idle' | 'waiting' }))] }))
 const original = apiFetch.getMockImplementation()!
 apiFetch.mockImplementation(async (url: string) => {
  if (url.includes('/board-cards/b-rel/view')) return json({ columns: [{ id: 'todo', name: 'To do' }], cards, releases: [] })
  const match = /\/board-cards\/b-rel\/(\d+)/.exec(url)
  if (match) return json({ card: { ...cards[Number(match[1]) - 1], column_name: 'To do', columns: [{ id: 'todo', name: 'To do' }] } })
  if (url.includes('/sess-a/live')) return json({ session_id: 'sess-a', through_seq: 0, generated_at: new Date().toISOString(), asks: [], subagents: [], tasks: [], counts: { needs_you: 0, running: 1 }, can_release_prompt: true, children: workers.map(w => ({ ...w, asking: w.status === 'waiting', engine: 'codex', model: '', started_at: '2026-10-27T00:00:00Z', tokens: null, ticket: { source: 'blerg-board', board_id: 'b-rel', key: `#${w.number}`, title: cards[w.number - 1].title } })) })
  return original(url)
 })
 mountWorkspaceApp()
 if (width === 390) await openOpenMenu()
 await userEvent.click(await screen.findByTestId('session-board-button'))
 const board = await screen.findByRole('region', { name: 'Board columns' })
 // A genuine workspace split keeps the board pane narrow within the 4K viewport.
 if (pane < width) {
  const tab = screen.getByRole('tab', { name: 'Release planning' })
  fireEvent.contextMenu(tab, { clientX: 250, clientY: 100 })
  fireEvent.click(screen.getByRole('menuitem', { name: 'Move to a split panel' }))
  await waitFor(() => expect(document.querySelectorAll('[data-group-id]')).toHaveLength(2))
  const group = board.closest<HTMLElement>('[data-group-id]')!
  group.style.width = `${pane}px`
  await waitFor(() => expect(Math.round(group.getBoundingClientRect().width)).toBe(pane))
 }
 await waitFor(() => expect(document.querySelectorAll('.session-board-workers button')).toHaveLength(3))
 expect(screen.queryByText(/Worker: Release worker/)).toBeNull()
 for (const w of workers) expect(screen.getByRole('button', { name: `${w.title}, ${w.status}` })).toBeVisible()
 expect(document.querySelectorAll('.session-board-card')[2].querySelector('.session-board-workers')).toBeNull()
 const shots = import.meta.env.VITE_BOARD_SHOTS_DIR
 const capture = async (state: string) => {
  if (!shots) return
  const frame = document.createElement('div')
  frame.style.cssText = 'position:fixed;inset:0;width:100vw;height:100vh;pointer-events:none'
  document.body.append(frame)
  try { await page.screenshot({ element: frame, path: `${shots}/blg505-${state}-${width}-pane-${pane}-${theme}.png` }) } finally { frame.remove() }
 }
 await capture('board')
 if (width !== 390) {
  screen.getByRole('button', { name: 'Release worker, running' }).focus()
  await page.getByRole('button', { name: 'Release worker, running' }).hover()
  expect(await screen.findByRole('tooltip')).toHaveTextContent('Release worker')
  await capture('hover')
  await userEvent.keyboard('{Escape}')
 }
 await userEvent.click(screen.getByRole('button', { name: /REL-2.*Review the migration/ }))
 const dialog = await screen.findByRole('dialog')
 await screen.findByTestId('board-card-live')
 expect(dialog.getBoundingClientRect().right).toBeLessThanOrEqual(width)
 // BLG-571: the owner's 2026-11-05 decision caps the ticket modal at min(1120px, 90vw),
 // relative to the viewport even when the board lives in a narrower split pane.
 if (width === 3840) {
  expect(dialog).toBe(screen.getByTestId('ticket-modal'))
  expect(dialog.getBoundingClientRect().width).toBe(Math.min(1120, width * 0.9))
 }
 await capture('dialog')
 await userEvent.keyboard('{Escape}')
 await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
 expect(screen.getByRole('tab', { name: 'Release planning' })).toBeVisible()
 await userEvent.click(screen.getByRole('button', { name: /REL-2.*Review the migration/ }))
 await userEvent.click(await screen.findByRole('button', { name: 'Open as tab' }))
 await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
 expect(screen.getByRole('tab', { name: 'REL-2' })).toHaveAttribute('aria-selected', 'true')
 expect(await screen.findByTestId('board-card-live')).toBeVisible()
 expect(screen.getByTestId('board-card-worker')).toContainElement(screen.getByRole('button', { name: 'Migration reviewer, idle' }))
 await capture('tab')
 await userEvent.click(screen.getByRole('button', { name: 'Migration reviewer, idle' }))
 expect(await screen.findByRole('tab', { name: 'Migration reviewer' })).toHaveAttribute('aria-selected', 'true')
})
