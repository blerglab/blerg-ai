// Card BLG-450, phase 1 + review round 2: the session top bar grouped by action — opens a tab
// (shell, board, Insights, blackboard), non-destructive (search, star, panel toggle), destructive
// (Stop, End, after a visibly wider gap) — with the "opens a tab" group collapsing into one Open
// menu below a narrow width (a phone, or a narrow split pane inside a wide window). Review round 2
// also moves Search and Star into that same Open menu on a phone (the title keeps the room
// instead), fixes the menu's row style to one plain shape (an icon, a label, quiet secondary text —
// never a link, never a separate caption line), and checks the header never overflows.
import { afterEach, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { useTranscriptStore } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import CommandPaletteProvider from './CommandPalette'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { invalidateChatTransport } from '../lib/chatTransport'
import { resetSessionBoardLinksForTest } from '../lib/sessionBoardLinks'
import { resetBoardOriginForTest } from '../lib/boardLink'
import * as wsMock from '../test/wsMock'
import '../index.css'
import '@blerglab/chat/blerg.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
vi.mock('../shellTransport', async original => {
  const actual = await original<typeof import('../shellTransport')>()
  const { FakeShell } = await import('../test/shellMock')
  return { ...actual, restoreShells: async () => [], openShell: vi.fn(async () => new FakeShell('tab-shell')) }
})
vi.mock('../lib/engineModels', async original => ({ ...await original<typeof import('../lib/engineModels')>(), useEngineModels: () => ({ models: [], loading: false, fallback: false, source: 'live', engineEfforts: [] }) }))
const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { 'Content-Type': 'application/json' } })
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals(); document.documentElement.removeAttribute('data-theme'); resetSessionBoardLinksForTest(); resetBoardOriginForTest() })

const LONG_TITLE = 'Ship the quarterly billing reconciliation feature'

function seed(options: { title?: string; board?: boolean; status?: 'running' | 'idle' | 'stopped' | 'waiting'; starred?: boolean; ask?: string; id?: string } = {}) {
  const id = options.id ?? 'sess-a'
  document.documentElement.setAttribute('data-theme', 'dark')
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ id, kind: 'agent', status: options.status ?? 'running', starred: options.starred ?? false, title: options.title ?? 'Ship the thing' })] })
  useTranscriptStore.setState({ sessions: {} })
  useTranscriptStore.getState().ingestReplayDone(id, { lastSeq: 0, hasMore: false })
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), location.origin)
    if (url.pathname.endsWith('/shell-capability')) return json({ available: true })
    if (options.board && url.pathname.endsWith('/permissions')) return json({ can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [{ board_id: 'b-rel', board_name: 'Release planning', level: 'full', launch: true }] })
    if (options.board && url.pathname === '/api/session-boards') return json({ boards: [{ id: 'b-rel', name: 'Release planning' }] })
    if (options.board && url.pathname === '/api/site-board') return json({ board_url: 'https://board.example.com' })
    if (url.pathname.endsWith('/live')) return json({
      session_id: 'sess-a', through_seq: 0, generated_at: new Date().toISOString(), tokens_hidden: true,
      asks: options.ask ? [{ event_id: 'ask-1', message_id: 'm1', question: options.ask, asked_at: new Date().toISOString(), seq: 1 }] : [],
      subagents: [], tasks: [], tool_changes: 0, counts: { needs_you: options.ask ? 1 : 0, running: 0 }, can_release_prompt: true, children: [],
    })
    return json({})
  }))
}

// `width`/`height` take a pixel number for a narrow pane inside a wider window (the collapse
// follows the pane, not the window), or the string 'full' for the ordinary case — the app filling
// its real window. 'full' renders at 100vw/100vh rather than a literal pixel number matching the
// current page.viewport(): a fixed pixel width can still leave the mounted tree short of the
// actual viewport (whatever margin/centering the test harness itself applies around the rendered
// tree), which is why the round-2 screenshot at 1440x900 only filled part of the window — 100vw/
// 100vh fills the real viewport regardless of any such offset (round 3 review, fix #2).
function mount(width: number | 'full', id = 'sess-a') {
  const size = width === 'full' ? { width: '100vw', height: '100vh' } : { width, height: 900 }
  return render(
    <MemoryRouter initialEntries={[`/sessions/${id}`]}>
      <CommandPaletteProvider>
        <div style={{ ...size, display: 'flex' }}>
          <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
        </div>
      </CommandPaletteProvider>
    </MemoryRouter>,
  )
}

/** Every row in the collapsed Open menu follows one shape (review round 2): an icon, then the
 *  label with its quiet secondary text underneath — a plain row whether it is a real `<a>` (the
 *  board opener, which still opens in a new tab) or a `<button>`, never styled as an underlined
 *  link, never a separate caption row of its own. */
function expectPlainMenuRow(row: HTMLElement) {
  expect(row).toHaveClass('icon-menu-item')
  expect(row.querySelector('.icon-menu-icon')).not.toBeNull()
  expect(row.querySelector('.icon-menu-text .icon-menu-label')).not.toBeNull()
  expect(getComputedStyle(row).textDecorationLine).toBe('none')
}

it('collapses the opens-a-tab group into one Open menu in a narrow split pane inside a wide window', async () => {
  await page.viewport(1920, 1080)
  seed()
  mount(520)
  const header = await screen.findByTestId('session-header-controls')
  const openButton = await within(header).findByTestId('open-tab-menu')
  expect(within(header).queryByRole('button', { name: 'New shell' })).toBeNull()
  expect(within(header).queryByRole('button', { name: 'Insights' })).toBeNull()
  // Search, Star and the panel toggle stay visible controls even while the group is collapsed: a
  // narrow pane inside a wide window keeps this app's own window-level chrome, so there is room.
  expect(within(header).getByRole('button', { name: 'Search commands and files' })).toBeVisible()
  expect(within(header).getByRole('button', { name: 'Star' })).toBeVisible()
  expect(within(header).getByTestId('rail-toggle')).toBeVisible()
  // Stop and End stay last, visible, after a wider gap than the other groups'.
  const stop = within(header).getByRole('button', { name: "Stop the agent's current turn" })
  const end = within(header).getByRole('button', { name: 'End workspace' })
  expect(stop.nextElementSibling).toBe(end)
  // No overflow in the narrow pane either.
  const row = screen.getByTestId('session-header')
  expect(row.scrollWidth).toBe(row.clientWidth)
  await userEvent.click(openButton)
  const menu = within(screen.getByRole('menu', { name: 'Open' }))
  expectPlainMenuRow(await menu.findByRole('menuitem', { name: 'New shell' }))
  expectPlainMenuRow(menu.getByRole('menuitem', { name: 'Insights' }))
  // Never disabled, even with no blackboard set up yet (round 3 review): it opens the empty state.
  expectPlainMenuRow(menu.getByRole('menuitem', { name: 'Open blackboard' }))
  // Search and Star stay their own header controls at this width — not duplicated into the menu.
  expect(menu.queryByRole('menuitem', { name: /Search commands/ })).toBeNull()
  expect(menu.queryByRole('menuitem', { name: /^Star$/ })).toBeNull()
})

it('keeps one plain row style for every item in the Open menu, including a real board link', async () => {
  await page.viewport(1920, 1080)
  seed({ board: true })
  mount(520)
  await userEvent.click(await screen.findByTestId('open-tab-menu'))
  const menu = within(screen.getByRole('menu', { name: 'Open' }))
  const shell = await menu.findByRole('menuitem', { name: 'New shell' })
  const board = await menu.findByRole('menuitem', { name: 'Open board: Release planning' })
  const insights = menu.getByRole('menuitem', { name: 'Insights' })
  const blackboard = menu.getByRole('menuitem', { name: /^Open blackboard/ })
  for (const row of [shell, board, insights, blackboard]) expectPlainMenuRow(row)
  // The board row is a real link (it opens the board in a new tab) but draws the same way as the
  // button rows beside it: the board's name is quiet secondary text under a plain "Open board"
  // label, not stuffed into one underlined line.
  expect(board.tagName).toBe('A')
  expect(board.querySelector('.icon-menu-label')!.textContent).toBe('Open board')
  expect(board.querySelector('.icon-menu-hint')!.textContent).toMatch(/Release planning/)
  const rows = [shell, board, insights, blackboard]
  for (const row of rows) expect(getComputedStyle(row).color).toBe(getComputedStyle(rows[0]).color)
})

it('collapses the opens-a-tab group AND search/star into one Open menu on a 390px phone, with no overflow', async () => {
  await page.viewport(390, 844)
  seed({ title: LONG_TITLE })
  mount('full')
  const header = await screen.findByTestId('session-header')
  const controls = await within(header).findByTestId('session-header-controls')
  // Tab-openers, Search and Star are all gone from the visible header now.
  expect(within(controls).queryByRole('button', { name: 'New shell' })).toBeNull()
  expect(within(controls).queryByRole('button', { name: 'Insights' })).toBeNull()
  expect(within(controls).queryByRole('button', { name: 'Open blackboard' })).toBeNull()
  expect(within(controls).queryByRole('button', { name: 'Search commands and files' })).toBeNull()
  expect(within(controls).queryByRole('button', { name: 'Star' })).toBeNull()
  // Only the panel toggle, then Stop and End (last), stay visible controls.
  const visible = [...controls.querySelectorAll<HTMLButtonElement>('button:not([role="menuitem"])')]
  expect(visible.map(b => b.getAttribute('aria-label'))).toEqual([
    'Open', 'Workspace panel', "Stop the agent's current turn", 'End workspace',
  ])
  // The title stays readable: the controls group carries the space pressure (fewer visible
  // controls now), not the title — it keeps a real, legible width, not squeezed down to a word or
  // two as it was before this round ("Shi…").
  const title = header.querySelector<HTMLElement>('div[style*="cursor: text"]')!
  expect(title.textContent).toBe(LONG_TITLE)
  expect(title.getBoundingClientRect().width).toBeGreaterThan(80)
  // Nothing overflows the header row at this width.
  expect(header.scrollWidth).toBe(header.clientWidth)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  // Search and Star open from inside the one Open menu.
  const openButton = within(controls).getByTestId('open-tab-menu')
  await userEvent.click(openButton)
  const menu = within(screen.getByRole('menu', { name: 'Open' }))
  expectPlainMenuRow(await menu.findByRole('menuitem', { name: 'New shell' }))
  expectPlainMenuRow(menu.getByRole('menuitem', { name: 'Search commands and files' }))
  expectPlainMenuRow(menu.getByRole('menuitem', { name: 'Star' }))
  expectPlainMenuRow(menu.getByRole('menuitem', { name: 'Layouts' }))
  await userEvent.click(menu.getByRole('menuitem', { name: 'New shell' }))
  await screen.findByRole('tab', { name: 'Shell' })
})

// Round 3 review, fix #1: the Open menu ran off the right edge at 390px (align="left" opens it
// rightward from a trigger that sits near the header's right edge at this width), with a
// horizontal scrollbar on the whole page as a result. 320px is the narrowest realistic phone
// width and is added here as a second tested width, not just 390.
for (const width of [390, 320]) {
  it(`keeps the page and the open Open menu inside the viewport at ${width}px`, async () => {
    await page.viewport(width, 844)
    seed({ title: LONG_TITLE, board: true })
    const view = mount('full')
    const controls = await screen.findByTestId('session-header-controls')
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(window.innerWidth)
    await userEvent.click(within(controls).getByTestId('open-tab-menu'))
    const menu = await screen.findByRole('menu', { name: 'Open' })
    const rect = menu.getBoundingClientRect()
    expect(rect.left).toBeGreaterThanOrEqual(0)
    expect(rect.right).toBeLessThanOrEqual(window.innerWidth)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(window.innerWidth)
    view.unmount()
  })
}

it('toggles the session star from inside the phone Open menu', async () => {
  await page.viewport(390, 844)
  seed()
  mount('full')
  await userEvent.click(await screen.findByTestId('open-tab-menu'))
  await userEvent.click(screen.getByRole('menuitem', { name: 'Star' }))
  expect(wsMock.send).toHaveBeenCalledWith({ type: 'set_session_star', session_id: 'sess-a', starred: true })
})

it('shows the full grouped layout at natural widths on a 3840px window, with no stretching', async () => {
  await page.viewport(3840, 2160)
  seed()
  mount('full')
  const header = await screen.findByTestId('session-header-controls')
  await waitFor(() => expect(within(header).queryByTestId('open-tab-menu')).toBeNull())
  const shell = await within(header).findByRole('button', { name: 'New shell' })
  const insights = within(header).getByRole('button', { name: 'Insights' })
  const blackboard = within(header).getByRole('button', { name: 'Open blackboard' })
  const search = within(header).getByRole('button', { name: 'Search commands and files' })
  const star = within(header).getByRole('button', { name: 'Star' })
  const layouts = within(header).getByRole('button', { name: 'Layouts' })
  const panel = within(header).getByTestId('rail-toggle')
  const stop = within(header).getByRole('button', { name: "Stop the agent's current turn" })
  const end = within(header).getByRole('button', { name: 'End workspace' })
  // Shell, board (absent: no board here), Insights, blackboard — in that order.
  expect(shell.nextElementSibling).toBe(insights)
  expect(insights.nextElementSibling).toBe(blackboard)
  expect(stop.nextElementSibling).toBe(end)
  const buttons = [shell, insights, blackboard, search, star, layouts, panel, stop, end]
  const rects = buttons.map(b => b.getBoundingClientRect())
  // Natural, un-stretched icon-button width — never stretched to fill the very wide window.
  for (const r of rects) { expect(r.width).toBeLessThan(80); expect(r.height).toBeLessThan(80) }
  // One row.
  for (const r of rects) expect(Math.abs(r.top - rects[0].top)).toBeLessThan(1)
  // A visibly wider gap before the destructive group than between the other groups.
  const insightsBlackboardGap = blackboard.getBoundingClientRect().left - insights.getBoundingClientRect().right
  const starPanelGap = panel.getBoundingClientRect().left - layouts.getBoundingClientRect().right
  const panelStopGap = stop.getBoundingClientRect().left - panel.getBoundingClientRect().right
  expect(panelStopGap).toBeGreaterThan(insightsBlackboardGap + 4)
  expect(panelStopGap).toBeGreaterThan(starPanelGap + 4)
  // A short, muted divider sits between each pair of groups (owner 2026-11-03), never at the start.
  const groups = [...header.querySelectorAll<HTMLElement>('.session-header-group')].filter(g => g.children.length > 0)
  expect(getComputedStyle(groups[0], '::before').content).toBe('none')
  for (const g of groups.slice(1)) {
   const divider = getComputedStyle(g, '::before')
   expect(divider.width).toBe('1px')
   expect(parseFloat(divider.height)).toBeLessThan(rects[0].height)
   expect(Number(divider.opacity)).toBeLessThan(1)
  }
})

// Review round 2's required screenshots: the real app, its real CSS, the default dark theme, at
// 3840x2160, 1440x900, 390x844, and a 520px pane inside a 1920px window — one of them with the
// Open menu expanded at 390 and one at the narrow-pane width. Set VITE_TOPBAR_SHOT_DIR to capture;
// skipped otherwise, so an ordinary run never writes files.
const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_TOPBAR_SHOT_DIR
if (shotDir) {
  it('captures the real top bar at the required viewports', async () => {
    seed({ title: LONG_TITLE, board: true })

    await page.viewport(3840, 2160)
    let view = mount('full')
    await screen.findByTestId('session-header-controls')
    await page.screenshot({ path: `${shotDir}/topbar-3840x2160.png` })
    view.unmount()

    await page.viewport(1440, 900)
    view = mount('full')
    await screen.findByTestId('session-header-controls')
    await page.screenshot({ path: `${shotDir}/topbar-1440x900.png` })
    view.unmount()

    await page.viewport(390, 844)
    view = mount('full')
    const controls390 = await screen.findByTestId('session-header-controls')
    await page.screenshot({ path: `${shotDir}/topbar-390x844.png` })
    await userEvent.click(within(controls390).getByTestId('open-tab-menu'))
    await screen.findByRole('menu', { name: 'Open' })
    await page.screenshot({ path: `${shotDir}/topbar-390x844-open-menu.png` })
    view.unmount()

    await page.viewport(1920, 1080)
    view = mount(520)
    const controlsPane = await screen.findByTestId('session-header-controls')
    await page.screenshot({ path: `${shotDir}/topbar-520-pane-in-1920.png` })
    await userEvent.click(within(controlsPane).getByTestId('open-tab-menu'))
    await screen.findByRole('menu', { name: 'Open' })
    await page.screenshot({ path: `${shotDir}/topbar-520-pane-in-1920-open-menu.png` })
    view.unmount()
  })

  // BLG-450 acceptance round: the top bar with each of four session states, at 390 and 3840 —
  // running, stopped, starred, and waiting-on-you with a real pending question (ux-design skill's
  // state checklist). The rail panel (open by default) carries the "Needs you" row for waiting.
  it('captures the top bar for running, stopped, starred and waiting sessions', async () => {
    for (const width of [390, 3840] as const) {
      const size = width === 390 ? { w: 390, h: 844 } : { w: 3840, h: 2160 }

      // Each state gets its own session id: useLiveStateServer keeps a per-session held reply
      // across remounts (card's min-read-gap cache), so reusing one id across these four seeds in
      // the same test would leak a later state's /live reply into an earlier screenshot.
      seed({ status: 'running', id: 'sess-running' })
      await page.viewport(size.w, size.h)
      let view = mount('full', 'sess-running')
      await screen.findByTestId('session-header-controls')
      await page.screenshot({ path: `${shotDir}/topbar-running-${width}.png` })
      view.unmount()

      seed({ status: 'stopped', id: 'sess-stopped' })
      await page.viewport(size.w, size.h)
      view = mount('full', 'sess-stopped')
      await screen.findByTestId('session-header-controls')
      await page.screenshot({ path: `${shotDir}/topbar-stopped-${width}.png` })
      view.unmount()

      seed({ starred: true, id: 'sess-starred' })
      await page.viewport(size.w, size.h)
      view = mount('full', 'sess-starred')
      const controls = await screen.findByTestId('session-header-controls')
      // At 390 Star collapses into the Open menu (round 2): open it so the starred state ("Unstar",
      // filled star) is actually visible in the shot, not hidden behind a closed menu trigger.
      const starButton = within(controls).queryByTestId('star-toggle')
      if (starButton) { await screen.findByTestId('star-toggle') } else {
        await userEvent.click(within(controls).getByTestId('open-tab-menu'))
        await screen.findByRole('menuitem', { name: 'Unstar' })
      }
      await page.screenshot({ path: `${shotDir}/topbar-starred-${width}.png` })
      view.unmount()

      seed({ status: 'waiting', ask: 'Deploy to production?', id: 'sess-waiting' })
      await page.viewport(size.w, size.h)
      view = mount('full', 'sess-waiting')
      await screen.findByTestId('session-header-controls')
      // The real pending-question state (ux-design skill's state checklist): a "Waiting for your
      // answer" pill from the live ask this seed() stubs in on /live.
      await screen.findByText('Waiting for your answer')
      await page.screenshot({ path: `${shotDir}/topbar-waiting-${width}.png` })
      view.unmount()
    }
  })
}
