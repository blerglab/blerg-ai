import { browserWidths } from '../test/browserWidths'
// The session header's controls in a real browser (card #87): one row of one icon-button style at a
// phone's width and on a 4K screen, the ⋯ menu inside the screen, and the app's one bell (in the
// sessions header) the same size as the header's buttons.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { commands, page, userEvent } from 'vitest/browser'
import { BrowserRouter, MemoryRouter, Route, Routes, useNavigate, useLocation } from 'react-router-dom'
import '../index.css'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'

vi.mock('../ws', () => import('../test/wsMock'))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

import { useTranscriptStore } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import CommandPaletteProvider from './CommandPalette'
import SessionList from './SessionList'

const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }))

beforeEach(() => {
  localStorage.clear()
  apiFetch.mockImplementation(() => json({}))
  vi.stubGlobal('fetch', vi.fn(() => json([])))
  useSessionStore.setState({
    daemons: [makeDaemon()],
    sessions: [makeSession({ id: 'sess-a', kind: 'agent', status: 'running', title: 'Ship the release notes for the next version of the app' })],
    statusChangedAt: {}, serverVersion: 'test',
  })
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

function mount(withSidebar: boolean) {
  return render(
    <MemoryRouter initialEntries={['/sessions/sess-a']}>
      <div style={{ display: 'flex', height: '100vh' }}>
        {withSidebar && <SessionList variant="sidebar" onNewSession={() => {}} />}
        <div style={{ flex: 1, minWidth: 0 }}>
          <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
        </div>
      </div>
    </MemoryRouter>,
  )
}

describe('the session header', () => {
  it.each(browserWidths([390, 3840]))('fits its controls on one row, in one style, at %i px', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    mount(width > 1000)
    const group = await screen.findByTestId('session-header-controls')
    await within(group).findByRole('button', { name: "Stop the agent's current turn" })
    const buttons = [...group.querySelectorAll<HTMLButtonElement>('button:not([role="menuitem"])')]
    // Search was added to this group after the original five-control layout test. Card BLG-450
    // grouped it by what each control does and moved the panel toggle in from the tab bar; below a
    // narrow width (this phone's included) shell/board/Insights/blackboard collapse into one Open
    // menu. Review round 2: on a phone, Search and Star join that same Open menu too (the title
    // keeps the room instead), so only the panel toggle, Stop and End stay controls there; on a
    // wide screen all of Search, Star, panel, Stop and End stay controls. Layouts also
    // lives in Open on phones and remains a standalone control on wide screens.
    // Name every control so missing, duplicated or unexpected actions still fail.
    expect(buttons).toEqual([
      ...(width > 1000 ? [within(group).getByRole('button', { name: 'Insights' }), within(group).getByRole('button', { name: 'Open blackboard' })] : [within(group).getByRole('button', { name: 'Open' })]),
      ...(width > 1000 ? [within(group).getByRole('button', { name: 'Search commands and files' }), within(group).getByRole('button', { name: 'Star' })] : []),
      ...(width > 1000 ? [within(group).getByRole('button', { name: 'Layouts' })] : []),
      within(group).getByTestId('rail-toggle'),
      // The session runs: Stop, immediately left of End session.
      within(group).getByRole('button', { name: "Stop the agent's current turn" }),
      within(group).getByRole('button', { name: 'End workspace' }),
    ])
    if (width <= 1000) {
      // Search, Star and Layouts remain reachable inside the collapsed Open menu.
      await userEvent.click(within(group).getByTestId('open-tab-menu'))
      const menu = within(screen.getByRole('menu', { name: 'Open' }))
      expect(menu.getByRole('menuitem', { name: 'Search commands and files' })).toBeVisible()
      expect(menu.getByRole('menuitem', { name: 'Star' })).toBeVisible()
      expect(menu.getByRole('menuitem', { name: 'Layouts' })).toBeVisible()
      await userEvent.keyboard('{Escape}')
    }
    // Exactly one Notifications bell on the page: in the header on a phone (no sidebar there), in
    // the sidebar beside + New on a wide screen.
    const bells = screen.queryAllByRole('button', { name: /^Notifications/ })
    expect(bells).toHaveLength(width > 1000 ? 1 : 0)
    if (bells[0]) expect(group.contains(bells[0])).toBe(false)
    const rects = buttons.map(b => b.getBoundingClientRect())
    // One row, inside the screen.
    for (const r of rects) {
      expect(Math.abs(r.top - rects[0].top)).toBeLessThan(1)
      expect(r.right).toBeLessThanOrEqual(width)
      expect(r.left).toBeGreaterThanOrEqual(0)
    }
    // One size and one outline (the panel toggle may widen for its count).
    for (const [i, b] of buttons.entries()) {
      const cs = getComputedStyle(b)
      expect(cs.borderTopWidth).toBe('1px')
      expect(cs.borderTopLeftRadius).toBe('6px')
      expect(rects[i].height).toBe(rects[0].height)
      expect(rects[i].width).toBe(rects[0].width)
      expect(b.querySelector('svg')!.getAttribute('stroke-width') ?? b.querySelector('svg')!.getAttribute('strokeWidth')).toBe('1.75')
    }
    // The title keeps room (review round 2 of card BLG-450: the controls group, not the title,
    // carries the space pressure — on a phone Search and Star move into the Open menu for exactly
    // this reason), and the header itself never overflows the viewport.
    const title = group.parentElement!.querySelector<HTMLElement>('div[style*="cursor: text"]')!
    expect(title.textContent).toMatch(/Ship the release notes/)
    expect(title.getBoundingClientRect().width).toBeGreaterThan(width > 1000 ? 100 : 70)
    expect(title.parentElement!.getBoundingClientRect().width).toBeGreaterThan(width > 1000 ? 600 : 90)
    const header = screen.getByTestId('session-header')
    expect(header.scrollWidth).toBe(header.clientWidth)
    expect(within(group).queryByRole('button', { name: 'Toggle explorer' })).toBeNull()
    expect(screen.queryByRole('toolbar', { name: 'Workspace tools' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'More actions' })).toBeNull()
    expect(within(group).queryByLabelText('Verbosity')).toBeNull()
    const folder = screen.getByRole('button', { name: 'Toggle explorer' })
    expect(folder.closest('nav')?.getAttribute('aria-label')).toBe(width < 900 ? 'Workspace navigation' : 'Workspace')
    const toggle = screen.getByTestId('rail-toggle')
    if (toggle.getAttribute('aria-expanded') !== 'true') fireEvent.click(toggle)
    const verbosity = await screen.findByLabelText('Verbosity')
    expect(verbosity.closest('.rail-options')).toBeTruthy()
    expect(verbosity.getBoundingClientRect().right).toBeLessThanOrEqual(width)
    expect(verbosity.getBoundingClientRect().left).toBeGreaterThanOrEqual(0)

  })

  // Card #128: an open / pressed control is the selected blue, never the accent (Blaze), which beside
  // the danger red read as a warning. Checked in both themes, at a phone's width and on a 4K screen.
  it.each([
    [390, 'light'], [390, 'dark'], [3840, 'light'], [3840, 'dark'],
  ] as const)('draws the open panel toggle in the selected blue, not the accent, at %i px (%s)', async (width, theme) => {
    document.documentElement.dataset.theme = theme
    try {
      await page.viewport(width, width === 3840 ? 2160 : 844)
      mount(width > 1000)
      const toggle = await screen.findByTestId('rail-toggle')
      if (toggle.getAttribute('aria-expanded') !== 'true') await userEvent.click(toggle)
      expect(toggle).toHaveAttribute('aria-expanded', 'true')
      // The theme's values, resolved by the browser the same way the toggle's are.
      const probe = document.createElement('div')
      document.body.append(probe)
      const resolve = (value: string) => { probe.style.color = value; return getComputedStyle(probe).color }
      const selected = resolve('var(--selected-border)')
      const accentValues = [resolve('var(--accent)'), resolve('var(--accent-border)'), resolve('var(--blaze)')]
      const danger = resolve('var(--danger)')
      probe.remove()
      expect(selected).toBe(theme === 'dark' ? 'rgb(76, 155, 255)' : 'rgb(47, 111, 214)')
      const cs = getComputedStyle(toggle)
      // (the icon buttons ease their colors: wait out the transition after the click)
      await expect.poll(() => cs.borderTopColor).toBe(selected)
      await expect.poll(() => cs.color).toBe(theme === 'dark' ? 'rgb(106, 173, 255)' : 'rgb(37, 89, 176)')
      expect(accentValues).not.toContain(cs.borderTopColor)
      expect(cs.borderTopColor).not.toBe(danger)
      // End session stays the only red control, at every width now (card BLG-450).
      expect(getComputedStyle(screen.getByTestId('stop-session')).color).toBe(danger)
    } finally {
      delete document.documentElement.dataset.theme
    }
  })

  it('draws the sidebar bell in the same style and size as the header buttons, its list inside the screen', async () => {
    await page.viewport(3840, 2160)
    mount(true)
    const bell = await screen.findByRole('button', { name: /^Notifications/ })
    const star = within(await screen.findByTestId('session-header-controls')).getByTestId('star-toggle')
    expect(bell).toHaveClass('icon-btn')
    expect(bell.getBoundingClientRect().height).toBe(star.getBoundingClientRect().height)
    expect(screen.getAllByRole('button', { name: /^Notifications/ })).toHaveLength(1)
    await userEvent.click(bell)
    const list = screen.getByRole('region', { name: 'Notifications' }).getBoundingClientRect()
    expect(list.left).toBeGreaterThanOrEqual(0)
    expect(list.right).toBeLessThanOrEqual(3840)
    expect(list.width).toBeGreaterThan(1400)
    expect(screen.getByTestId('push-setting')).toBeVisible()
  })
})

it.each([390, 3840])('keeps a worker link, Main/channel selection and the header together at %i px', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  localStorage.clear()
  const main = makeSession({ id: 'main', kind: 'agent', title: 'Release workspace', status: 'idle', runtime: 'daemon' })
  const worker = makeSession({ id: 'worker', kind: 'agent', title: 'Performance pass', status: 'idle', runtime: 'daemon', parent_session_id: 'main', started_by: { kind: 'session', name: main.title } })
  const channel = makeSession({ id: 'channel', kind: 'agent', title: 'Release notes', status: 'idle', parent_session_id: 'main' })
  useSessionStore.setState({ sessions: [main, worker, channel], daemons: [makeDaemon()] })
  useTranscriptStore.setState({ sessions: {} })
  for (const session of [main, worker, channel]) {
    useTranscriptStore.getState().ingest({ type: 'agent_event', session_id: session.id, client_event_id: 'greeting', seq: 1, ts: '2026-11-06T12:00:00Z', kind: 'assistant_text', payload: { text: `${session.title} is ready.`, done: true } })
    useTranscriptStore.getState().ingestReplayDone(session.id, { lastSeq: 1, hasMore: false })
  }
  apiFetch.mockImplementation((input: string) => {
    if (input.endsWith('/live')) return json({ session_id: input.split('/')[3], through_seq: 0, generated_at: new Date().toISOString(), asks: [], subagents: [], tasks: [], tool_changes: 0, counts: { needs_you: 0, running: 0 }, can_release_prompt: true })
    if (input.endsWith('/permissions')) return json({ can_grant: true, grants: [], boards: [], mcp_tools: [] })
    if (input.startsWith('/api/models/')) return json({ models: [], source: 'live' })
    if (input.endsWith('/shells')) return json([])
    return json({})
  })
  function Navigation() {
    const navigate = useNavigate()
    return <div hidden><output data-testid="route">{useLocation().pathname}</output><button onClick={() => navigate('/sessions/worker')}>Open worker conversation</button></div>
  }
  const originalURL = location.pathname + location.search
  history.replaceState(null, '', '/sessions/main')
  const mountWorkspace = () => render(<BrowserRouter><CommandPaletteProvider><Navigation /><div style={{ height: 'calc(100vh - 30px)' }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div></CommandPaletteProvider></BrowserRouter>)
  let mounted = mountWorkspace()
  fireEvent.keyDown(window, { key: 'Enter', ctrlKey: true, shiftKey: true })
  expect(screen.getByTestId('tab-focus-announce')).toHaveTextContent('Entered tab focus mode')
  // Use the same router navigation as Needs you / review conversation links.
  fireEvent.click(screen.getByText('Open worker conversation'))
  await expect.poll(() => screen.getByRole('tab', { name: 'Performance pass' }).getAttribute('aria-selected')).toBe('true')
  expect(screen.getByTestId('session-header')).toHaveTextContent('Performance pass')
  expect(screen.getByTestId('detail-started-by')).toHaveTextContent('started by')
  expect(screen.getByTestId('tab-focus-announce')).toHaveTextContent('Entered tab focus mode')
  fireEvent.keyDown(window, { key: 'Enter', ctrlKey: true, shiftKey: true })
  expect(document.querySelector('.session-transcript-pane .reveal-controls')).toBeNull()
  // Reload lifecycle: discard all components and restore the workspace layout from storage,
  // keeping the browser's URL and history state exactly as a reload does.
  mounted.unmount()
  mounted = mountWorkspace()
  expect(screen.getByRole('tab', { name: 'Performance pass' })).toHaveAttribute('aria-selected', 'true')
  expect(screen.getByTestId('session-header')).toHaveTextContent('Performance pass')
  const shots = import.meta.env.VITE_TAB_SHOT_DIR
  if (shots) {
    document.documentElement.dataset.theme = 'colorblind-dark'
    await commands.layoutScreenshot(width, 'colorblind-dark', 'workspace')
    delete document.documentElement.dataset.theme
  }
  fireEvent.click(width === 390 ? screen.getByRole('button', { name: 'Back to Main' }) : screen.getByRole('tab', { name: 'Main' }))
  await expect.poll(() => screen.getByTestId('route').textContent).toBe('/sessions/main')
  expect(screen.getByTestId('session-header')).toHaveTextContent('Release workspace')
  expect(screen.queryByTestId('detail-started-by')).toBeNull()
  history.back()
  await expect.poll(() => screen.getByTestId('route').textContent).toBe('/sessions/worker')
  await expect.poll(() => screen.getByRole('tab', { name: 'Performance pass' }).getAttribute('aria-selected')).toBe('true')
  history.forward()
  await expect.poll(() => screen.getByTestId('route').textContent).toBe('/sessions/main')
  if (shots) await commands.layoutScreenshot(width, 'light', 'workspace')
  // The Channels path must select the same tab and update the route, including on phones.
  const folder = screen.getByRole('button', { name: 'Toggle explorer' })
  if (folder.getAttribute('aria-expanded') !== 'true') fireEvent.click(folder)
  fireEvent.click(await screen.findByRole('treeitem', { name: 'Release notes' }))
  await expect.poll(() => screen.getByTestId('route').textContent).toBe('/sessions/channel')
  expect(screen.getByTestId('session-header')).toHaveTextContent('Release notes')
  if (width > 1000) expect(screen.getByRole('treeitem', { name: 'Release notes' })).toHaveAttribute('aria-selected', 'true')
  fireEvent.click(screen.getByRole('tab', { name: 'Main' }))
  await expect.poll(() => screen.getByTestId('route').textContent).toBe('/sessions/main')
  fireEvent.click(screen.getByRole('tab', { name: 'Performance pass' }))
  if (width === 390) fireEvent.click(screen.getByRole('button', { name: 'Open' }))
  fireEvent.click(screen.getByRole(width === 390 ? 'menuitem' : 'button', { name: 'Layouts' }))
  fireEvent.change(screen.getByRole('textbox', { name: 'Layout name' }), { target: { value: 'Review work' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))
  fireEvent.click(screen.getByRole('button', { name: 'Reset layout' }))
  await expect.poll(() => screen.getByTestId('route').textContent).toBe('/sessions/main')
  if (width === 390) fireEvent.click(screen.getByRole('button', { name: 'Open' }))
  fireEvent.click(screen.getByRole(width === 390 ? 'menuitem' : 'button', { name: 'Layouts' }))
  fireEvent.click(screen.getByRole('button', { name: 'Load Review work' }))
  await expect.poll(() => screen.getByTestId('route').textContent).toBe('/sessions/worker')
  expect(screen.getByRole('tab', { name: 'Performance pass' })).toHaveAttribute('aria-selected', 'true')
  expect(screen.getByTestId('session-header')).toHaveTextContent('Performance pass')
  mounted.unmount()
  history.replaceState(null, '', originalURL)
})
