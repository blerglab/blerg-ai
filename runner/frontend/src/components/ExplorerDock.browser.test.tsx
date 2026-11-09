import {railReady} from '../test/railReady'
import { browserWidths } from '../test/browserWidths'
import { useEffect, Profiler } from 'react'
import { useLocation } from 'react-router-dom'
import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { cdp, page, userEvent } from 'vitest/browser'
import { HOVERCARD, useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import { usePalette } from '../commands/context'
import type { CommandRegistry } from '../commands/registry'
import SessionDockWindows from './SessionDockWindows'
import WorkspaceTree from './WorkspaceTree'
import SessionDetail from './SessionDetail'
import SessionList from './SessionList'
import SessionTiles from './SessionTiles'
import { installHoverClock } from '../test/hoverClock'
import CommandPaletteProvider from './CommandPalette'
import { useSessionStore } from '../hooks/useSessionStore'
import { explorerPrefKey } from '../hooks/useExplorerLayout'
import { makeDaemon, makeSession } from '../test/fixtures'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'
import { invalidateChatTransport } from '../lib/chatTransport'
import { send } from '../test/wsMock'
import '../index.css'
import '@blerglab/chat/blerg.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../lib/boardLink', async original => ({ ...await original<typeof import('../lib/boardLink')>(), loadBoardOrigin: async () => 'https://board.example.com' }))
vi.mock('../lib/engineModels', async original => ({
  ...await original<typeof import('../lib/engineModels')>(),
  useEngineModels: () => ({ models: [{ id: 'model-next', name: 'Next', efforts: ['low', 'high'], default_effort: 'low' }], loading: false, fallback: false, source: 'live', engineEfforts: ['low', 'high'] }),
}))
const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_DOCK_SHOT_DIR
const rootEntries = [
  { name: 'app', path: 'app', directory: true, repository: true, member_id: 'app' },
  { name: 'docs', path: 'docs', directory: true },
  { name: 'README.md', path: 'README.md', directory: false },
  { name: 'package.json', path: 'package.json', directory: false },
]
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
beforeEach(() => {
  localStorage.clear()
  localStorage.setItem('blerg-runner.session-dock.windows', JSON.stringify({ active: 'files', panels: [], ratio: .5 }))
  document.documentElement.setAttribute('data-theme', 'dark')
  useTranscriptStore.setState({ sessions: {} })
  const transcript = useTranscriptStore.getState()
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'dock-user', seq: 1, ts: '2026-10-24T12:00:00Z', kind: 'user_message', payload: { text: 'Add the explorer beside the chat and keep the published files in the panel.', source: 'chat' } } as AgentEvent)
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'dock-assistant', seq: 2, ts: '2026-10-24T12:00:01Z', kind: 'assistant_text', payload: { text: 'The workspace tree is available from the folder control on the left activity rail. You can resize it with the divider, browse repositories with the keyboard, and open files in the shared viewer.', done: true } } as AgentEvent)
  transcript.ingestReplayDone('sess-a', { lastSeq: 2, hasMore: false })
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ kind: 'agent', status: 'idle', title: 'Build the workspace explorer' })] })
  localStorage.setItem(RAIL_PREF_KEY, '1')
  localStorage.setItem(RAIL_TAB_KEY, 'files')
  apiFetch.mockReset().mockImplementation(async (url: string) => {
    if (url.endsWith('/topics')) return json({ topics: [], unmarked: null, strokes: [] })
    if (url.includes('/workspace?')) {
      const query = new URL(url, 'https://example.com').searchParams
      const path = query.get('path'), op = query.get('op')
      return json({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: op === 'search' ? [rootEntries[2]] : path === 'app' ? [{ name: 'src', path: 'app/src', directory: true }, { name: 'index.ts', path: 'app/index.ts', directory: false }, { name: 'assets', path: 'app/assets', directory: true }] : path === 'app/src' ? [{ name: 'component.ts', path: 'app/src/component.ts', directory: false }] : path === 'app/assets' ? [{ name: 'logo.svg', path: 'app/assets/logo.svg', directory: false }] : rootEntries, truncated: false, ...(op === 'status' ? { status: { branch: 'main', changed: 3, untracked: 1, files: [{ path: 'index.ts', code: 'M' }, { path: 'src/component.ts', code: 'A' }, { path: 'removed.txt', code: 'D' }, { path: 'assets', code: 'U', directory: true }] } } : {}) }, members: [{ id: 'app', name: 'app', path: 'app' }], runtime: 'host' })
    }
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn(async (url: string) => String(url).includes('/workspace-file')
    ? json({ path: 'README.md', view: 'text', content_type: 'text/plain', content_b64: btoa('Workspace viewer fixture') })
    : json(String(url).endsWith('/artifacts') ? { artifacts: [{ id: 'report', name: 'report.md', size: 42, view: 'markdown', content_type: 'text/markdown', version: 1, latest_version: 1 }] } : [])))
})
afterEach(() => { vi.restoreAllMocks(); invalidateChatTransport(); vi.unstubAllGlobals(); document.documentElement.removeAttribute('data-theme') })
const reviewCommands: { registry: CommandRegistry | null } = { registry: null }
function ReviewCommands() { const palette = usePalette(); useEffect(() => { reviewCommands.registry = palette?.registry ?? null; return () => { reviewCommands.registry = null } }, [palette]); return null }
function RouteSearch() { const location = useLocation(); return <span hidden data-testid="route-search">{location.search}</span> }
function mount(width: number, initial = '/sessions/sess-a') {
  return render(<MemoryRouter initialEntries={[initial]}><CommandPaletteProvider><RouteSearch /><ReviewCommands />
    <div data-testid="dock-layout" style={{ display: 'flex', height: '100vh' }}>
      {width >= 900 && <SessionList variant="sidebar" onNewSession={() => {}} />}
      <div style={{ flex: 1, minWidth: 0 }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div>
    </div>
  </CommandPaletteProvider></MemoryRouter>)
}
const toggle = () => screen.getByRole('button', { name: 'Toggle explorer' })
const isOpen = () => !!document.querySelector('.explorer-dock')
async function openExplorer() {
  await userEvent.click(toggle())
}
const chat = () => screen.getByTestId('session-chat-column')
const box = (element: Element) => element.getBoundingClientRect()
const geometry = (element: Element) => { const r = box(element); return [r.x, r.y, r.width, r.height] }
const noOverflow = (width: number) => expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)

for (const [width, height] of [[3840, 2160], [1440, 900]]) {
  it(`docks between list and chat, resizes without moving the panel, persists and uses 4K space at ${width}`, async () => {
    await page.viewport(width, height)
    const view = mount(width)
    const panel = await screen.findByTestId('session-rail')
    const beforePanel = geometry(panel), beforeChat = box(chat())
    const sidebar = screen.getByTestId('dock-layout').firstElementChild!
    const sidebarBox = geometry(sidebar)
    await openExplorer()
    const dock = screen.getByRole('region', { name: 'Explorer' })
    await screen.findByRole('treeitem', { name: /README/ })
    if (shotDir) {
      await userEvent.click(screen.getByRole('treeitem', { name: /^app(?:,|$)/ }))
      await screen.findByText('main · 4 changed')
      await userEvent.click(screen.getByRole('treeitem', { name: /^src(?:,|$)/ }))
      await screen.findByRole('treeitem', { name: /^component\.ts(?:,|$)/ })
      screen.getByRole('combobox', { name: 'Search workspace files' }).focus()
      await page.screenshot({ path: `${shotDir}/dock-${width}x${height}.png` })
    }
    const initialWidth = box(dock).width
    expect(initialWidth).toBeGreaterThan(width === 3840 ? 700 : 275)
    expect(box(dock).right).toBe(box(chat()).left)
    expect(beforeChat.width - box(chat()).width).toBe(initialWidth)
    expect(geometry(panel)).toEqual(beforePanel)
    expect(geometry(sidebar)).toEqual(sidebarBox)
    expect(box(sidebar).right).toBe(box(screen.getByRole('navigation', { name: 'Workspace' })).left)
    expect(document.activeElement).toBe(screen.getByRole('combobox', { name: 'Search workspace files' }))
    const handle = screen.getByRole('separator', { name: 'Explorer width' })
    // Real pointer drag (captured by the divider), beyond the old 340 px panel width.
    const start = box(handle)
    await userEvent.dragAndDrop(handle, chat(), { sourcePosition: { x: start.width / 2, y: 150 }, targetPosition: { x: 240, y: 150 } })
    await waitFor(() => expect(box(dock).width).toBeGreaterThan(initialWidth + 100))
    expect(geometry(panel)).toEqual(beforePanel)
    expect(box(dock).right).toBe(box(chat()).left)
    handle.focus()
    await userEvent.keyboard('{End}')
    expect(box(dock).width).toBe(Number(handle.getAttribute('aria-valuemax')))
    if (width === 3840) expect(box(dock).width).toBe(1536)
    expect(box(chat()).width).toBeGreaterThan(200)
    await userEvent.keyboard('{Home}{ArrowRight}')
    const savedWidth = box(dock).width
    const key = explorerPrefKey('person-a') + '.sess-a'
    expect(JSON.parse(localStorage.getItem(key)!)).toEqual({ open: true, width: savedWidth })
    view.unmount(); mount(width)
    const restored = screen.getByRole('region', { name: 'Explorer' })
    expect(box(restored).width).toBe(savedWidth)
    expect(isOpen()).toBe(true)
    expect(localStorage.getItem(RAIL_PREF_KEY)).toBe('1')
    expect(box(screen.getByTestId('session-rail')).width).toBe(beforePanel[2])
    noOverflow(width)
    await userEvent.click(width < 768 ? screen.getByRole('button', { name: 'Collapse explorer' }) : toggle())
    expect(screen.queryByRole('region', { name: 'Explorer' })).toBeNull()
    expect(document.activeElement).toBe(toggle())
    expect(geometry(screen.getByTestId('session-rail'))).toEqual(beforePanel)
    expect(box(chat()).width).toBe(beforeChat.width)
  })
}
for (const [width, height] of [[390, 844], [3840, 2160]]) {
  it(`toggles from registry, leaves browser shortcuts alone, navigates the tree and preserves quick open at ${width}`, async () => {
    await page.viewport(width, height); mount(width)
    toggle().focus()
    await userEvent.keyboard('{Control>}{Shift>}e{/Shift}{/Control}')
    expect(isOpen()).toBe(false)
    await openExplorer()
    const src = await screen.findByRole('treeitem', { name: /app/ })
    src.focus()
    await userEvent.keyboard('{ArrowRight}')
    await screen.findByRole('treeitem', { name: /index.ts/ })
    await userEvent.keyboard('{ArrowRight}')
    expect(document.activeElement?.textContent).toContain('src')
    await userEvent.keyboard('{ArrowLeft}')
    expect(document.activeElement).toBe(src)
    await userEvent.keyboard('{End}')
    expect(document.activeElement?.textContent).toContain('package.json')
    await userEvent.keyboard('{Meta>}{Shift>}e{/Shift}{/Meta}')
    expect(isOpen()).toBe(true)
    await userEvent.click(width < 768 ? screen.getByRole('button', { name: 'Collapse explorer' }) : toggle())
    expect(isOpen()).toBe(false)
    expect(document.activeElement).toBe(toggle())
    // On a phone (review round 2 of card BLG-450), Search joins Star inside the collapsed Open menu.
    if (width < 768) await userEvent.click(screen.getByTestId('open-tab-menu'))
    await userEvent.click(screen.getByRole(width < 768 ? 'menuitem' : 'button', { name: 'Search commands and files' }))
    const palette = screen.getByRole('dialog', { name: 'Command palette' })
    await userEvent.fill(within(palette).getByRole('combobox'), '>Toggle explorer')
    await userEvent.click(within(palette).getByRole('option', { name: /Toggle explorer/ }))
    await screen.findByRole('tree')
    expect(screen.queryByRole('dialog', { name: 'Command palette' })).toBeNull()
    await userEvent.click(width < 768 ? screen.getByRole('button', { name: 'Collapse explorer' }) : toggle())
    await userEvent.keyboard('{Control>}p{/Control}')
    expect(screen.getByRole('dialog', { name: 'Quick open' })).toBeTruthy()
    await userEvent.keyboard('{Escape}')
    noOverflow(width)
  })
}
it('opens a 390px full-screen sheet without changing chat or panel layout, traps focus and opens the shared viewer', async () => {
  await page.viewport(390, 844)
  const view = mount(390)
  const beforeChat = geometry(chat())
  expect(screen.queryByTestId('session-rail')).toBeNull()
  expect(screen.queryByRole('toolbar', { name: 'Workspace tools' })).toBeNull()
  expect(toggle()).toBeVisible()
  toggle().focus()
  if (shotDir) await page.screenshot({ path: `${shotDir}/rail-menu-390x844.png` })
  await userEvent.keyboard('{Enter}')
  const sheet = screen.getByRole('dialog', { name: 'Explorer' })
  const close = within(sheet).getByRole('button', { name: 'Collapse explorer' })
  expect(geometry(sheet)).toEqual([0, 0, 390, 844])
  expect(geometry(chat())).toEqual(beforeChat)
  expect(document.activeElement).toBe(screen.getByRole('combobox', { name: 'Search workspace files' }))
  expect(document.querySelector<HTMLElement>('[data-session-root]')!.inert).toBe(true)
  // The phone sheet has no width handle (the composer's own resize handle is a separator too).
  expect(screen.queryByRole('separator', { name: 'Explorer width' })).toBeNull()
  const file = await within(sheet).findByRole('treeitem', { name: /README/ })
  // Shift+Tab from the edge control wraps to the tree's single tab stop.
  close.focus()
  await userEvent.keyboard('{Shift>}{Tab}{/Shift}')
  expect(document.activeElement?.getAttribute('role')).toBe('treeitem')
  await userEvent.keyboard('{Tab}')
  expect(document.activeElement).toBe(close)
  noOverflow(390)
  if (shotDir) {
    await userEvent.click(screen.getByRole('treeitem', { name: /^app(?:,|$)/ })); await screen.findByText('main · 4 changed')
    await userEvent.click(screen.getByRole('treeitem', { name: /^src(?:,|$)/ })); await screen.findByRole('treeitem', { name: /^component\.ts(?:,|$)/ })
    await page.screenshot({ path: `${shotDir}/dock-390x844.png` })
  }
  await userEvent.click(file)
  await screen.findByRole('region', { name: 'README.md' })
  await screen.findByText('Workspace viewer fixture')
  expect(screen.queryByRole('dialog', { name: 'Explorer' })).toBeNull()
  expect(document.querySelector<HTMLElement>('[data-session-root]')!.inert).toBe(false)
  await userEvent.click(screen.getByRole('button', { name: 'Close README.md' }))
  await openExplorer()
  await userEvent.keyboard('{Escape}')
  expect(screen.queryByRole('dialog', { name: 'Explorer' })).toBeNull()
  expect(document.activeElement).toBe(toggle())
  expect(geometry(chat())).toEqual(beforeChat)
  expect(localStorage.getItem(RAIL_PREF_KEY)).toBe('1')
  view.unmount(); mount(390)
  expect(isOpen()).toBe(false)
  await openExplorer()
  expect(geometry(screen.getByRole('dialog', { name: 'Explorer' }))).toEqual([0, 0, 390, 844])
})

it('retains the desktop width through a phone sheet and keeps the open phone preference across remounts', async () => {
  await page.viewport(3840, 2160)
  const first = mount(3840)
  await openExplorer()
  const divider = screen.getByRole('separator', { name: 'Explorer width' })
  divider.focus(); await userEvent.keyboard('{End}')
  const saved = localStorage.getItem(explorerPrefKey('person-a'))
  await page.viewport(390, 844)
  await waitFor(() => expect(screen.getByRole('dialog', { name: 'Explorer' })).toBeTruthy())
  expect(geometry(screen.getByRole('dialog', { name: 'Explorer' }))).toEqual([0, 0, 390, 844])
  expect(localStorage.getItem(explorerPrefKey('person-a'))).toBe(saved)
  first.unmount()
  const second = mount(390)
  expect(screen.getByRole('dialog', { name: 'Explorer' })).toBeTruthy()
  await page.viewport(3840, 2160)
  await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Explorer' })).toBeNull())
  expect(box(screen.getByRole('region', { name: 'Explorer' })).width).toBe(1536)
  expect(localStorage.getItem(explorerPrefKey('person-a'))).toBe(saved)
  second.unmount()
})

for (const [width, height] of [[390, 844], [3840, 2160]]) {
  it(`shows file status and folder rollups with shared icons, one search, and stable refresh focus at ${width}`, async () => {
    await page.viewport(width, height); mount(width)
    await openExplorer()
    const explorer = screen.getByRole(width === 390 ? 'dialog' : 'region', { name: 'Explorer' })
    await userEvent.click(await screen.findByRole('treeitem', { name: /^app(?:,|$)/ }))
    await screen.findByText('main · 4 changed')
    const app = screen.getByRole('treeitem', { name: /^app(?:,|$)/ })
    expect(within(app).getByTitle('Modified in this folder').textContent).toBe('M')
    expect(within(app).getByTitle('Added in this folder').textContent).toBe('A')
    const modified = within(screen.getByRole('treeitem', { name: /^index\.ts(?:,|$)/ })).getByTitle('Modified')
    const deleted = within(screen.getByRole('treeitem', { name: /^removed\.txt(?:,|$)/ })).getByTitle('Deleted')
    expect(modified.textContent).toBe('M'); expect(deleted.textContent).toBe('D')
    const probe = document.createElement('span'); probe.style.color = 'var(--chat-diff-del)'; explorer.append(probe)
    expect(getComputedStyle(deleted).color).toBe(getComputedStyle(probe).color); probe.remove()
    await userEvent.click(screen.getByRole('treeitem', { name: /^src(?:,|$)/ }))
    const added = await screen.findByRole('treeitem', { name: /^component\.ts(?:,|$)/ })
    expect(within(added).getByTitle('Added').textContent).toBe('A')
    expect(getComputedStyle(added.closest('[role="group"]')!).borderInlineStartWidth).toBe('1px')
    expect(added.querySelector('svg.artifact-glyph')?.getAttribute('data-file-icon')).toBe('typescript')
    expect(explorer.textContent).not.toContain('◆')
    expect(explorer.querySelectorAll('.workspace-chevron')).not.toHaveLength(0)
    expect([...explorer.querySelectorAll('.workspace-chevron')].some(span => span.textContent === '·')).toBe(false)
    await userEvent.click(screen.getByRole('treeitem', { name: /^assets(?:,|$)/ }))
    expect(within(await screen.findByRole('treeitem', { name: /^logo\.svg(?:,|$)/ })).getByTitle('Untracked').textContent).toBe('U')
    const search = within(explorer).getByRole('combobox', { name: 'Search workspace files' })
    expect(within(explorer).getAllByRole('combobox')).toHaveLength(1)
    await userEvent.fill(search, 'component')
    expect(screen.queryByRole('treeitem', { name: /^index\.ts(?:,|$)/ })).toBeNull()
    expect(screen.getByRole('option', { name: /component\.ts/ })).toBeTruthy()
    const before = apiFetch.mock.calls.filter(([url]) => String(url).includes('op=search')).length
    expect(before).toBe(0)
    await userEvent.fill(search, 'README'); await userEvent.keyboard('{Enter}')
    await screen.findByText('Name matches')
    expect(apiFetch.mock.calls.some(([url]) => String(url).includes('op=search') && String(url).includes('filter=README'))).toBe(true)
    const refresh = screen.getByRole('button', { name: 'Refresh workspace' })
    const baseRead = apiFetch.getMockImplementation()!
    let release: (() => void) | undefined
    apiFetch.mockImplementationOnce((...args: unknown[]) => new Promise(resolve => { release = () => resolve(baseRead(...args)) }))
    refresh.focus(); await userEvent.keyboard('{Enter}')
    expect(document.activeElement).toBe(refresh)
    expect(refresh.getAttribute('aria-disabled')).toBe('true')
    release!()
    await screen.findByRole('treeitem', { name: 'README.md' })
    expect(document.activeElement).toBe(refresh)
    expect(explorer.contains(document.activeElement)).toBe(true)
    const refreshedFile = screen.getByRole('treeitem', { name: 'README.md' })
    await userEvent.click(refreshedFile)
    await screen.findByTestId('workspace-viewer-overlay')
    await screen.findByText('Workspace viewer fixture')
    const closeViewer = screen.getByRole('button', { name: 'Close README.md' })
    const closeBox = box(closeViewer)
    expect(closeViewer.contains(document.elementFromPoint(closeBox.x + closeBox.width / 2, closeBox.y + closeBox.height / 2))).toBe(true)
    await userEvent.click(closeViewer)
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: /Main/ }))
    if (width === 390) await openExplorer()
    expect(explorer.querySelector('h2')).toBeNull()
    expect(getComputedStyle(screen.getByRole(width === 390 ? 'dialog' : 'region', { name: 'Explorer' })).backgroundColor).toBe(getComputedStyle(document.querySelector('[data-session-root]')!).backgroundColor)
    noOverflow(width)
  })
  it(`leaves editable inputs and browser Ctrl/Command+Shift+E unclaimed at ${width}`, async () => {
    await page.viewport(width, height); mount(width)
    await openExplorer(); await screen.findByRole('tree')
    const input = screen.getByRole('combobox', { name: 'Search workspace files' }); input.focus()
    await userEvent.keyboard('{Control>}{Shift>}e{/Shift}{/Control}{Meta>}{Shift>}e{/Shift}{/Meta}')
    expect(document.activeElement).toBe(input)
    expect(isOpen()).toBe(true)
    for (const tag of ['textarea', 'div']) {
      const editable = document.createElement(tag)
      if (tag === 'div') { editable.contentEditable = 'true'; editable.setAttribute('role', 'textbox') }
      input.parentElement!.append(editable); editable.focus()
      const event = new KeyboardEvent('keydown', { key: 'E', ctrlKey: true, shiftKey: true, bubbles: true, cancelable: true })
      editable.dispatchEvent(event)
      expect(event.defaultPrevented).toBe(false)
      expect(document.activeElement).toBe(editable)
      expect(isOpen()).toBe(true)
      editable.remove()
    }
  })
}

it('reserves usable chat at 1024px, including saved widths, panel choices and viewport changes', async () => {
  await page.viewport(3840, 900); mount(3840)
  await openExplorer(); screen.getByRole('separator', { name: 'Explorer width' }).focus(); await userEvent.keyboard('{End}')
  expect(box(screen.getByRole('region', { name: 'Explorer' })).width).toBe(1536)
  await page.viewport(1024, 900)
  await waitFor(() => expect(box(chat()).width).toBeGreaterThanOrEqual(320))
  const columns = screen.getByTestId('session-columns')
  await waitFor(() => expect(columns.scrollWidth).toBe(columns.clientWidth))
  expect(getComputedStyle(columns).overflowX).toBe('hidden')
  expect(screen.queryByTestId('session-rail')).toBeNull()
  noOverflow(1024)
  await userEvent.click(screen.getByTestId('rail-toggle'))
  expect(screen.getByRole('dialog', { name: 'Workspace panel' })).toBeVisible()
  await userEvent.keyboard('{Escape}')
  screen.getByRole('separator', { name: 'Explorer width' }).focus(); await userEvent.keyboard('{End}')
  expect(box(chat()).width).toBeGreaterThanOrEqual(320)
  expect(columns.scrollWidth).toBe(columns.clientWidth)
  noOverflow(1024)
  await page.viewport(3840, 900)
  screen.getByRole('separator', { name: 'Explorer width' }).focus(); await userEvent.keyboard('{End}')
  await waitFor(() => expect(box(screen.getByRole('region', { name: 'Explorer' })).width).toBe(1536))
})

it('collapses by dragging to the left edge and reopens from the activity rail at 3840', async () => {
  await page.viewport(3840, 2160); mount(3840)
  await openExplorer()
  const dock = screen.getByRole('region', { name: 'Explorer' })
  const handle = screen.getByRole('separator', { name: 'Explorer width' })
  await userEvent.dragAndDrop(handle, dock, { sourcePosition: { x: 4, y: 100 }, targetPosition: { x: 10, y: 100 } })
  expect(screen.queryByRole('region', { name: 'Explorer' })).toBeNull()
  expect(document.activeElement).toBe(toggle())
  await openExplorer()
  expect(screen.getByRole('region', { name: 'Explorer' })).toBeTruthy()
})

for (const [width, height] of [[390, 844], [3840, 2160]]) it(`shows legacy terminal history without live input at ${width}`, async () => {
  await page.viewport(width, height)
  useSessionStore.setState({ sessions: [makeSession({ kind: 'tmux', status: 'running' })] })
  mount(width)
  expect(screen.getByRole('region', { name: 'Terminal history' })).toBeTruthy()
  expect(screen.getByText('Terminal sessions have been removed. Saved history is read-only.')).toBeTruthy()
  expect(document.querySelector('textarea')).toBeNull()
  expect(screen.queryByRole('button', { name: 'New shell' })).toBeNull()
  if (width === 3840) expect(screen.getByRole('region', { name: 'Workspace list' })).toBeTruthy()
  expect(isOpen()).toBe(false)
})

type AccessibilityNode = { role?: { value: string }; name?: { value: string }; properties?: { name: string; value: { value?: unknown } }[] }
type FrameTree = { frame: { id: string; url: string }; childFrames?: FrameTree[] }
async function accessibilityNodes() {
  const client = cdp() as unknown as { send(method: string, params?: object): Promise<unknown> }
  const { frameTree } = await client.send('Page.getFrameTree') as { frameTree: FrameTree }
  function currentFrame(tree: FrameTree): string | undefined {
    if (tree.frame.url === window.location.href) return tree.frame.id
    for (const child of tree.childFrames ?? []) { const found = currentFrame(child); if (found) return found }
  }
  const frameId = currentFrame(frameTree)
  expect(frameId).toBeTruthy()
  return (await client.send('Accessibility.getFullAXTree', { frameId }) as { nodes: AccessibilityNode[] }).nodes
}
for (const [width, height] of [[390, 844], [3840, 2160]]) {
  it(`announces file status and folder rollups in the focused accessible row at ${width}`, async () => {
    await page.viewport(width, height); mount(width); await openExplorer()
    await userEvent.click(await screen.findByRole('treeitem', { name: /^app(?:,|$)/ }))
    await screen.findByText('main · 4 changed')
    const modified = screen.getByRole('treeitem', { name: 'index.ts, modified' })
    modified.focus()
    let nodes = await accessibilityNodes()
    const focused = nodes.find(node => node.role?.value === 'treeitem' && node.name?.value === 'index.ts, modified')
    expect(focused).toBeTruthy()
    expect(focused?.properties?.some(property => property.name === 'focused' && property.value.value === true)).toBe(true)
    expect(nodes.some(node => node.role?.value === 'treeitem' && node.name?.value === 'removed.txt, deleted')).toBe(true)
    const parent = screen.getByRole('treeitem', { name: /app, modified in this folder, added in this folder, untracked in this folder, deleted in this folder/ })
    parent.focus(); nodes = await accessibilityNodes()
    expect(nodes.some(node => node.role?.value === 'treeitem' && node.name?.value === parent.getAttribute('aria-label') && node.properties?.some(property => property.name === 'focused' && property.value.value === true))).toBe(true)
  })
  it(`keeps names readable with capped guides through 28 nested directories at ${width}`, async () => {
    await page.viewport(width, height)
    // Branching folders retain depth: single-child chains now compact by default.
    const leafName = 'a-very-long-file-name-'.repeat(18) + 'leaf.txt'
    apiFetch.mockImplementation(async (url: string) => {
      if (!url.includes('/workspace?')) return json({})
      const path = new URL(url, 'https://example.com').searchParams.get('path') ?? ''
      const depth = path ? path.split('/').length : 0
      const name = depth === 28 ? leafName : `level-${depth}`
      return json({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: [{ name, path: path ? `${path}/${name}` : name, directory: depth < 28 }, ...depth < 28 ? [{ name: 'sibling.txt', path: path ? `${path}/sibling.txt` : 'sibling.txt', directory: false }] : []], truncated: false }, members: [], runtime: 'host' })
    })
    mount(width); await openExplorer()
    if (width === 3840) { screen.getByRole('separator', { name: 'Explorer width' }).focus(); await userEvent.keyboard('{Home}') }
    for (let depth = 0; depth < 28; depth++) await userEvent.click(await screen.findByRole('treeitem', { name: `level-${depth}` }))
    const leaf = await screen.findByRole('treeitem', { name: leafName })
    leaf.focus()
    const name = leaf.querySelector<HTMLElement>('.workspace-name')!
    const body = document.querySelector<HTMLElement>('.explorer-body')!
    expect(leaf.getAttribute('aria-level')).toBe('29')
    expect(box(name).width).toBeGreaterThan(100)
    expect(name.title).toBe(leafName)
    expect(name.scrollWidth).toBeGreaterThan(name.clientWidth)
    expect(getComputedStyle(name).textOverflow).toBe('ellipsis')
    expect(box(name).right).toBeLessThanOrEqual(box(body).right + 1)
    expect(body.scrollWidth).toBeLessThanOrEqual(body.clientWidth + 1)
    const groups = [...body.querySelectorAll<HTMLElement>('[role="group"]')]
    expect(groups.filter(group => parseFloat(getComputedStyle(group).marginInlineStart) > 0)).toHaveLength(4)
    await waitFor(() => expect(screen.getAllByRole('treeitem').filter(row => row.tabIndex === 0)).toEqual([leaf]))
    if (width === 3840) expect(box(screen.getByRole('region', { name: 'Explorer' })).width).toBe(307)
    noOverflow(width)
  })
  it(`links the search combobox to its listbox and keeps focus through keyboard selection at ${width}`, async () => {
    await page.viewport(width, height); mount(width); await openExplorer()
    await screen.findByRole('treeitem', { name: 'README.md' })
    let input = screen.getByRole('combobox', { name: 'Search workspace files' })
    expect(input.getAttribute('aria-expanded')).toBe('false')
    const controlled = document.getElementById(input.getAttribute('aria-controls')!)!
    expect(controlled).toBeTruthy()
    expect(controlled.getAttribute('role')).toBe('listbox')
    expect(input.getAttribute('aria-autocomplete')).toBe('list')
    await userEvent.fill(input, 'README')
    const results = screen.getByRole('listbox', { name: 'Workspace search results' })
    expect(results).toBe(controlled)
    expect(input.getAttribute('aria-expanded')).toBe('true')
    expect(input.hasAttribute('aria-activedescendant')).toBe(false)
    expect(within(results).getByRole('option', { name: 'README.md' })).toBeTruthy()
    expect(apiFetch.mock.calls.some(([url]) => String(url).includes('op=search'))).toBe(false)
    await userEvent.keyboard('{ArrowDown}')
    const active = document.getElementById(input.getAttribute('aria-activedescendant')!)!
    expect(active).toBeTruthy()
    expect(active.getAttribute('role')).toBe('option')
    expect(active.getAttribute('aria-selected')).toBe('true')
    expect(active.textContent).toContain('README.md')
    expect(document.activeElement).toBe(input)
    const nodes = await accessibilityNodes()
    expect(nodes.some(node => node.role?.value === 'combobox' && node.name?.value === 'Search workspace files' && node.properties?.some(property => property.name === 'expanded' && property.value.value === true))).toBe(true)
    await userEvent.keyboard('{Enter}')
    await screen.findByText('Workspace viewer fixture')
    expect(apiFetch.mock.calls.some(([url]) => String(url).includes('op=search'))).toBe(false)
    await userEvent.keyboard('{Escape}')
    if (width === 390) expect(document.querySelector('.agent-composer textarea')).not.toHaveFocus()
    else expect(document.activeElement).toBe(document.querySelector('.agent-composer textarea'))
    if (width === 390) await openExplorer()
    input = screen.getByRole('combobox', { name: 'Search workspace files' })
    await userEvent.fill(input, '')
    await userEvent.fill(input, 'README')
    input.focus()
    await userEvent.keyboard('{Escape}')
    await userEvent.keyboard('{ArrowUp}')
    expect(input.getAttribute('aria-expanded')).toBe('true')
    expect(document.getElementById(input.getAttribute('aria-activedescendant')!)?.getAttribute('role')).toBe('option')
    await userEvent.keyboard('{Escape}')
    expect(input.getAttribute('aria-expanded')).toBe('false')
    expect(input.hasAttribute('aria-activedescendant')).toBe(false)
    expect(document.activeElement).toBe(input)
    expect(isOpen()).toBe(true)
    await userEvent.fill(input, 'README'); await userEvent.keyboard('{Enter}')
    await screen.findByText('Name matches')
    expect(apiFetch.mock.calls.some(([url]) => String(url).includes('op=search'))).toBe(true)
    expect(input.getAttribute('aria-expanded')).toBe('true')
    expect(within(results).getByRole('option', { name: 'README.md' })).toBeTruthy()
  })
}

for (const [width, height] of [[390, 844], [3840, 2160]]) {
  it(`enters expanded app/src with nested Right Arrow at ${width}`, async () => {
    await page.viewport(width, height); mount(width)
    await openExplorer()
    const app = await screen.findByRole('treeitem', { name: /^app(?:,|$)/ })
    await userEvent.click(app)
    const src = await screen.findByRole('treeitem', { name: /^src(?:,|$)/ })
    await userEvent.click(src)
    const component = await screen.findByRole('treeitem', { name: /^component\.ts(?:,|$)/ })
    expect(src.getAttribute('aria-expanded')).toBe('true')
    src.focus()
    await userEvent.keyboard('{ArrowRight}')
    expect(document.activeElement).toBe(component)
    expect(component.getAttribute('aria-level')).toBe('3')
    await userEvent.keyboard('{ArrowLeft}')
    expect(document.activeElement).toBe(src)
    await userEvent.keyboard('{ArrowUp}')
    expect(document.activeElement).toBe(app)
    await userEvent.keyboard('{ArrowDown}')
    expect(document.activeElement).toBe(src)
    await userEvent.keyboard('{ArrowDown}')
    expect(document.activeElement).toBe(component)
    await userEvent.keyboard('{Home}')
    expect(document.activeElement).toBe(app)
    await userEvent.keyboard('{ArrowUp}')
    expect(document.activeElement).toBe(app)
    const last = screen.getByRole('treeitem', { name: 'package.json' })
    await userEvent.keyboard('{End}')
    expect(document.activeElement).toBe(last)
    await userEvent.keyboard('{ArrowDown}')
    expect(document.activeElement).toBe(last)
    noOverflow(width)
  })

  it(`reenters the leaf after a 28-folder collapse/expand with nested Right Arrow at ${width}`, async () => {
    // Siblings keep the nested keyboard regression independent of folder compaction.
    await page.viewport(width, height)
    apiFetch.mockImplementation(async (url: string) => {
      if (!url.includes('/workspace?')) return json({})
      const path = new URL(url, 'https://example.com').searchParams.get('path') ?? ''
      const depth = path ? path.split('/').length : 0
      const name = depth === 28 ? 'leaf.txt' : `folder${depth}`
      return json({ result: { ok: true, launch_generation: 'launch', generation: 1,
        entries: [{ name, path: path ? `${path}/${name}` : name, directory: depth < 28 }, ...depth < 28 ? [{ name: 'sibling.txt', path: path ? `${path}/sibling.txt` : 'sibling.txt', directory: false }] : []], truncated: false }, members: [], runtime: 'host' })
    })
    mount(width)
    await openExplorer()
    for (let depth = 0; depth < 28; depth++) {
      await userEvent.click(await screen.findByRole('treeitem', { name: `folder${depth}` }))
    }
    const leaf = await screen.findByRole('treeitem', { name: 'leaf.txt' })
    const parent = screen.getByRole('treeitem', { name: 'folder27' })
    leaf.focus()
    await userEvent.keyboard('{ArrowLeft}')
    expect(document.activeElement).toBe(parent)
    expect(parent.getAttribute('aria-level')).toBe('28')
    await userEvent.keyboard('{ArrowLeft}')
    expect(parent.getAttribute('aria-expanded')).toBe('false')
    expect(screen.queryByRole('treeitem', { name: 'leaf.txt' })).toBeNull()
    await userEvent.keyboard('{ArrowRight}')
    expect(parent.getAttribute('aria-expanded')).toBe('true')
    const restoredLeaf = await screen.findByRole('treeitem', { name: 'leaf.txt' })
    expect(document.activeElement).toBe(parent)
    await userEvent.keyboard('{ArrowRight}')
    expect(document.activeElement).toBe(restoredLeaf)
    expect(restoredLeaf.getAttribute('aria-level')).toBe('29')
    await waitFor(() => expect(screen.getAllByRole('treeitem').filter(row => row.tabIndex === 0)).toEqual([restoredLeaf]))
    await userEvent.keyboard('{ArrowUp}')
    expect(document.activeElement).toBe(parent)
    await userEvent.keyboard('{ArrowDown}')
    expect(document.activeElement).toBe(restoredLeaf)
    await userEvent.keyboard('{Home}')
    expect(document.activeElement).toBe(screen.getByRole('treeitem', { name: 'folder0' }))
    await userEvent.keyboard('{End}')
    expect(document.activeElement).toBe(screen.getAllByRole('treeitem').at(-1))
    noOverflow(width)
  })
}

for (const [width, height] of [[1440, 900], [3840, 2160]]) {
  it(`keeps the tab bar above the dock with keyboard toggling and stable columns at ${width}`, async () => {
    await page.viewport(width, height); mount(width)
    const rail = screen.getByRole('navigation', { name: 'Workspace' })
    const button = within(rail).getByRole('button', { name: 'Toggle explorer' })
    expect(within(screen.getByTestId('session-header-controls')).queryByRole('button', { name: 'Toggle explorer' })).toBeNull()
    const beforeRail = geometry(rail), beforeChat = geometry(chat())
    const beforePanel = geometry(screen.getByTestId('session-rail'))
    expect(box(rail).height).toBe(42)
    expect(box(rail).bottom).toBe(box(chat()).top)
    button.focus(); await userEvent.keyboard('{Enter}')
    const dock = screen.getByRole('region', { name: 'Explorer' })
    expect(button.getAttribute('aria-expanded')).toBe('true')
    expect(button.getAttribute('aria-controls')).toBe(dock.id)
    expect(geometry(rail)).toEqual(beforeRail)
    expect(box(rail).left).toBe(box(dock).left)
    expect(box(rail).bottom).toBe(box(dock).top)
    expect(box(dock).right).toBe(box(chat()).left)
    expect(geometry(screen.getByTestId('session-rail'))).toEqual(beforePanel)
    expect(rail.contains(document.elementFromPoint(box(button).x + 16, box(button).y + 16))).toBe(true)
    button.focus(); await userEvent.keyboard(' ')
    expect(isOpen()).toBe(false)
    expect(document.activeElement).toBe(button)
    expect(geometry(rail)).toEqual(beforeRail)
    expect(geometry(chat())).toEqual(beforeChat)
    expect(geometry(screen.getByTestId('session-rail'))).toEqual(beforePanel)
    for (const open of [true, false]) {
      button.focus()
      await userEvent.keyboard('{Control>}k{/Control}')
      const palette = screen.getByRole('dialog', { name: 'Command palette' })
      await userEvent.fill(within(palette).getByRole('combobox'), '>Toggle explorer')
      await userEvent.keyboard('{Enter}')
      expect(isOpen()).toBe(open)
    }
    expect(document.activeElement).toBe(button)
    noOverflow(width)
  })
}

async function command(title: string) {
  toggle().focus()
  await userEvent.keyboard('{Control>}k{/Control}')
  const palette = screen.getByRole('dialog', { name: 'Command palette' })
  await userEvent.fill(within(palette).getByRole('combobox'), `>${title}`)
  await userEvent.keyboard('{Enter}')
}
for (const [width, height] of [[1440, 900], [3840, 2160]]) {
  it(`shares session-list and explorer pane controls, resets widths and keeps rail actions at ${width}`, async () => {
    await page.viewport(width, height); mount(width)
    const list = screen.getByRole('region', { name: 'Workspace list' })
    const listDivider = screen.getByRole('separator', { name: 'Workspace list width' })
    const initial = box(list).width
    listDivider.focus(); await userEvent.keyboard('{ArrowRight}{ArrowRight}')
    expect(box(list).width).toBe(initial + 40)
    await userEvent.dragAndDrop(listDivider, chat(), { sourcePosition: { x: 4, y: 150 }, targetPosition: { x: 100, y: 150 } })
    expect(box(list).width).toBeGreaterThan(initial + 40)
    await userEvent.dblClick(listDivider)
    expect(box(list).width).toBe(initial)
    await openExplorer()
    const explorer = screen.getByRole('region', { name: 'Explorer' })
    const divider = screen.getByRole('separator', { name: 'Explorer width' })
    const initialExplorer = box(explorer).width
    divider.focus(); await userEvent.keyboard('{ArrowRight}')
    expect(box(explorer).width).toBe(initialExplorer + 20)
    await userEvent.dblClick(divider)
    expect(box(explorer).width).toBe(initialExplorer)
    expect(within(explorer).queryByRole('button', { name: 'Collapse explorer' })).toBeNull()
    toggle().focus(); await userEvent.keyboard('{Control>}b{/Control}')
    expect(screen.queryByRole('region', { name: 'Workspace list' })).toBeNull()
    const rail = screen.getByRole('toolbar', { name: 'Workspace list tools' })
    expect(box(rail).width).toBe(40)
    expect(within(rail).getByRole('button', { name: 'New workspace' })).toBeVisible()
    expect(within(rail).getByRole('button', { name: /^Notifications/ })).toBeVisible()
    const reopen = within(rail).getByRole('button', { name: 'Toggle workspace list' })
    expect(document.activeElement).toBe(reopen)
    if (shotDir) await page.screenshot({ path: `${shotDir}/session-list-rail-${width}x${height}.png` })
    await userEvent.click(reopen)
    expect(box(screen.getByRole('region', { name: 'Workspace list' })).width).toBe(initial)
    toggle().focus(); await userEvent.keyboard('{Meta>}b{/Meta}')
    expect(screen.queryByRole('region', { name: 'Workspace list' })).toBeNull()
    await command('Toggle workspace list')
    expect(screen.getByRole('region', { name: 'Workspace list' })).toBeTruthy()
    await page.viewport(1024, 900)
    expect(screen.getByRole('region', { name: 'Workspace list' })).toBeTruthy()
    expect(screen.getByRole('region', { name: 'Explorer' })).toBeTruthy()
    noOverflow(1024)
  })
}
for (const panelOpen of [true, false]) it(`restores the prior panes after Focus mode with panel open=${panelOpen}`, async () => {
  await page.viewport(3840, 2160); mount(3840)
  if (!panelOpen) await userEvent.click(screen.getByTestId('rail-toggle'))
  await openExplorer()
  const beforeChat = geometry(chat())
  const explorerWidth = box(screen.getByRole('region', { name: 'Explorer' })).width
  const listWidth = box(screen.getByRole('region', { name: 'Workspace list' })).width
  await command('Focus mode')
  expect(screen.queryByRole('region', { name: 'Workspace list' })).toBeNull()
  expect(screen.queryByRole('region', { name: 'Explorer' })).toBeNull()
  expect(screen.getByTestId('rail-toggle').getAttribute('aria-expanded')).toBe('false')
  expect(box(chat()).width).toBe(3840 - 40)
  if (shotDir) await page.screenshot({ path: `${shotDir}/focus-mode-3840x2160.png` })
  await command('Focus mode')
  expect(box(screen.getByRole('region', { name: 'Workspace list' })).width).toBe(listWidth)
  expect(box(screen.getByRole('region', { name: 'Explorer' })).width).toBe(explorerWidth)
  expect(screen.getByTestId('rail-toggle').getAttribute('aria-expanded')).toBe(String(panelOpen))
  expect(geometry(chat())).toEqual(beforeChat)
})
it('toggles from ordinary search fields and preserves unrelated shortcuts', async () => {
  await page.viewport(3840, 2160); mount(3840); await openExplorer()
  const input = screen.getByRole('combobox', { name: 'Search workspace files' })
  input.focus(); await userEvent.keyboard('{Control>}b{/Control}')
  expect(screen.queryByRole('region', { name: 'Workspace list' })).toBeNull()
  input.focus(); await userEvent.keyboard('{Meta>}b{/Meta}')
  expect(screen.getByRole('region', { name: 'Workspace list' })).toBeTruthy()
  const listBefore = box(screen.getByRole('region', { name: 'Workspace list' })).width
  toggle().focus(); await userEvent.keyboard('{Control>}{Alt>}b{/Alt}{/Control}{Control>}j{/Control}')
  expect(box(screen.getByRole('region', { name: 'Workspace list' })).width).toBe(listBefore)
})
for (const modifier of ['Control', 'Meta']) it(`${modifier}+B works from an ordinary composer`, async () => {
  await page.viewport(1440, 900); mount(1440)
  const composer = document.querySelector<HTMLTextAreaElement>('[data-testid="agent-chat"] textarea')!
  expect(composer).toBeTruthy(); composer.focus()
  await userEvent.keyboard(`{${modifier}>}b{/${modifier}}`)
  expect(screen.queryByRole('region', { name: 'Workspace list' })).toBeNull()
})

for (const surface of ['[data-palette-shortcuts="ignore"]', '.xterm', '.terminal-dom-surface', '.monaco-editor', '.cm-editor', '.CodeMirror', '.ace_editor', 'shadow editor', 'claimed field', 'modal']) {
  it(`leaves Ctrl/Command+B with its owner: ${surface}`, async () => {
    await page.viewport(3840, 2160); mount(3840)
    const host = document.createElement('div')
    if (surface.startsWith('.')) host.className = surface.slice(1)
    if (surface.startsWith('[') || surface === 'shadow editor') host.setAttribute('data-palette-shortcuts', 'ignore')
    if (surface === 'modal') { host.setAttribute('role', 'dialog'); host.setAttribute('aria-modal', 'true') }
    const input = document.createElement(surface === 'claimed field' || surface === 'modal' ? 'textarea' : 'button')
    input.textContent = 'Owned surface'
    if (surface === 'shadow editor') host.attachShadow({ mode: 'open' }).append(input)
    else host.append(input)
    if (surface === 'claimed field') input.addEventListener('keydown', event => event.preventDefault())
    document.body.append(host)
    try {
      input.focus()
      for (const modifier of ['Control', 'Meta']) {
        await userEvent.keyboard(`{${modifier}>}b{/${modifier}}`)
        expect(screen.getByRole('region', { name: 'Workspace list' })).toBeTruthy()
        expect(host.shadowRoot?.activeElement ?? document.activeElement).toBe(input)
      }
    } finally { host.remove() }
  })
}

for (const [width, height] of [[1440, 900], [3840, 2160]]) {
  it(`uses one collapsed rail with keyboard session tiles and pinned controls at ${width}`, async () => {
    await page.viewport(width, height)
    useSessionStore.setState({ sessions: [
      makeSession({ id: 'sess-a', kind: 'agent', title: 'Build the workspace explorer', status: 'idle', starred: true }),
      makeSession({ id: 'child', parent_session_id: 'sess-a', title: 'Child checks', status: 'running' }),
      ...Array.from({ length: 50 }, (_, n) => makeSession({ id: `tile-${n}`, title: `Project ${n}`, repo: 'example-org/project', status: n === 0 ? 'running' : 'idle' })),
    ] })
    mount(width)
    await userEvent.click(screen.getByRole('button', { name: 'Collapse workspace list' }))
    const rail = screen.getByRole('toolbar', { name: 'Workspace list tools' })
    expect(box(rail).width).toBe(40)
    expect(screen.queryByRole('toolbar', { name: 'Workspace tools' })).toBeNull()
    const folder = within(screen.getByRole('navigation', { name: 'Workspace' })).getByRole('button', { name: 'Toggle explorer' })
    expect(folder).toBeVisible()
    const tiles = within(rail).getByRole('group', { name: 'Workspaces' })
    expect(tiles.scrollHeight).toBeGreaterThan(tiles.clientHeight)
    const first = within(tiles).getAllByRole('button')[0]
    expect(first).toHaveAttribute('aria-current', 'page')
    expect(first.textContent).toContain('BT')
    expect(box(first).width).toBe(32)
    first.focus(); await userEvent.keyboard('{ArrowDown}')
    expect(document.activeElement).toBe(within(tiles).getAllByRole('button')[1])
    await userEvent.keyboard('{End}')
    expect(document.activeElement).toBe(within(tiles).getAllByRole('button').at(-1))
    expect(within(rail).getByRole('button', { name: 'New workspace' })).toBeVisible()
    expect(within(rail).getByRole('button', { name: /^Notifications/ })).toBeVisible()
    await userEvent.click(folder)
    expect(screen.getByRole('region', { name: 'Explorer' })).toBeVisible()
    expect(screen.queryByRole('toolbar', { name: 'Workspace tools' })).toBeNull()
    if (shotDir) { tiles.scrollTop = 0; await page.screenshot({ path: `${shotDir}/panes-${width}x${height}.png` }) }
    await userEvent.click(screen.getByRole('button', { name: 'Toggle workspace list' }))
    expect(within(screen.getByRole('navigation', { name: 'Workspace' })).getByRole('button', { name: 'Toggle explorer' })).toBeVisible()
    noOverflow(width)
  })
  it(`shares panel resize, reset, collapse, palette and persistence at ${width}`, async () => {
    await page.viewport(width, height)
    const view = mount(width)
    const panel = await screen.findByTestId('session-rail')
    const divider = screen.getByRole('separator', { name: 'Workspace panel width' })
    const top = box(panel).top
    divider.focus(); await userEvent.keyboard('{ArrowLeft}')
    expect(box(panel).width).toBe(360)
    await userEvent.dragAndDrop(divider, chat(), { sourcePosition: { x: 4, y: 150 }, targetPosition: { x: box(chat()).width - 90, y: 150 } })
    const saved = box(panel).width
    expect(saved).toBeGreaterThan(360)
    const chatGeometry = geometry(chat())
    view.unmount(); mount(width)
    expect(box(await screen.findByTestId('session-rail')).width).toBe(saved)
    expect(geometry(chat())).toEqual(chatGeometry)
    await userEvent.dblClick(screen.getByRole('separator', { name: 'Workspace panel width' }))
    expect(box(screen.getByTestId('session-rail')).width).toBe(340)
    expect(box(screen.getByTestId('session-rail')).top).toBe(top)
    const close = screen.getByTestId('rail-toggle')
    expect(close).toHaveAttribute('aria-controls', screen.getByTestId('session-rail').id)
    await userEvent.click(close)
    const reopen = screen.getByTestId('rail-toggle')
    expect(reopen).toHaveFocus()
    expect(screen.getByTestId('session-rail')).not.toBeVisible()
    await userEvent.keyboard('{Enter}')
    expect(screen.getByTestId('session-rail')).toBeVisible()
    await command('Toggle workspace panel')
    expect(screen.getByTestId('session-rail')).not.toBeVisible()
    await command('Toggle workspace panel')
    expect(screen.getByTestId('session-rail')).toBeVisible()
    noOverflow(width)
  })
}

it('keeps the chosen session panel open on a narrow desktop and only uses sheets on phones', async () => {
  await page.viewport(1440, 900)
  mount(1440)
  expect(await screen.findByTestId('session-rail')).toBeVisible()
  await page.viewport(960, 900)
  expect(screen.getByRole('region', { name: 'Workspace list' })).toBeVisible()
  expect(screen.getByTestId('session-rail')).toBeVisible()
  expect(screen.getByRole('separator', { name: 'Workspace panel width' })).toBeVisible()
  expect(screen.queryByRole('dialog', { name: 'Workspace panel' })).toBeNull()
  noOverflow(960)
  await userEvent.click(screen.getByTestId('rail-toggle'))
  await page.viewport(1440, 900)
  expect(screen.getByTestId('session-rail')).not.toBeVisible()
  expect(screen.getByTestId('rail-toggle')).toBeVisible()
})
for (const [width, height] of [[390, 844], [3840, 2160]]) {
  it(`retries a resumed workspace and announces progress at ${width}px`, async () => {
    await page.viewport(width, height)
    let recovered = false
    let finish!: (reply: Response) => void
    apiFetch.mockImplementation(async (url: string) => {
      if (!url.includes('/workspace?')) return json({})
      if (!recovered) return new Response(JSON.stringify({ error: "The session's daemon is disconnected. Reconnect it, then Refresh." }), { status: 409 })
      return new Promise<Response>(resolve => { finish = resolve })
    })
    mount(width)
    await openExplorer()
    await screen.findByText("The session's daemon is disconnected. Reconnect it, then Refresh.")
    recovered = true
    await userEvent.click(screen.getByRole('button', { name: 'Refresh workspace' }))
    await screen.findByText('Refreshing workspace…')
    finish(json({ result: { ok: true, launch_generation: 'legacy-session', generation: 1, entries: [{ name: 'README.md', path: 'README.md', directory: false }], truncated: false }, members: [], runtime: 'host' }))
    await screen.findByText('Workspace refreshed.')
    await screen.findByRole('treeitem', { name: 'README.md' })
    noOverflow(width)
  })
}

for (const [width, height] of [[1440, 900], [3840, 2160]]) {
  for (const worker of [false, true]) it(`previews the full ${worker ? 'standalone agent workspace with unread' : 'person workspace with unread'} card at ${width}px`, async () => {
    await page.viewport(width, height)
    useSessionStore.setState({ sessions: [
      makeSession({ id: 'sess-a', title: 'Build the workspace explorer', kind: 'tmux', status: 'error', error_reason: 'The agent process exited with code 73', runtime: 'daemon', unread: true, ...(worker ? { started_by: { kind: 'agent', name: 'Build automation' } } : {}) }),
    ] })
    mount(width)
    await userEvent.click(screen.getByRole('button', { name: 'Collapse workspace list' }))
    const tile = screen.getByRole('button', { name: 'Build the workspace explorer, error, unread' })
    expect(within(tile).getByTestId('status-dot')).toHaveAttribute('title', 'error')
    const glyph = within(tile).getByTestId('status-dot').getBoundingClientRect()
    const dot = tile.querySelector('.session-tile-unread')!.getBoundingClientRect()
    expect(dot.width).toBe(5)
    expect(dot.bottom).toBeLessThan(glyph.top)
    await userEvent.hover(tile)
    const preview = await screen.findByTestId('hovercard')
    expect(within(preview).getByTestId('unread-dot')).toBeVisible()
    expect(within(preview).getByTestId('error-reason')).toHaveTextContent('The agent process exited with code 73')
    expect(within(preview).getByTestId('kind-chip')).toHaveTextContent('Terminal')
    if (worker) expect(within(preview).getByTestId('started-by-chip')).toHaveTextContent('Build automation')
    else expect(within(preview).queryByTestId('started-by-chip')).toBeNull()
    expect(within(preview).getByTestId('runtime-chip').querySelector('[data-runtime]')).toHaveAttribute('data-runtime','host')
    expect(preview.getBoundingClientRect().left).toBeGreaterThanOrEqual(0)
    expect(preview.getBoundingClientRect().right).toBeLessThanOrEqual(width)
    if (shotDir) await page.screenshot({ path: `${shotDir}/tile-hovercard-${width}x${height}.png` })
    noOverflow(width)
  })
}

for (const [width, height] of [[390, 844], [1440, 900], [3840, 2160]]) {
  it(`actual Tab focus inside a full card survives the close delay at ${width}px`, async () => {
    await page.viewport(width, height)
    const clock = installHoverClock()
    useSessionStore.setState({ sessions: [
      makeSession({ id: 'keyboard', title: 'Keyboard preview' }),
      makeSession({ id: 'child', parent_session_id: 'keyboard', title: 'Preview child' }),
    ] })
    const view = render(<MemoryRouter><SessionTiles /></MemoryRouter>)
    try {
      await userEvent.keyboard('{Tab}')
      const tile = screen.getByRole('button', { name: /Keyboard preview, / })
      expect(tile).toHaveFocus()
      act(() => clock.advance(HOVERCARD.openDelayMs))
      const preview = screen.getByTestId('hovercard')
      const action = within(preview).getByRole('button', { name: 'Actions for Keyboard preview' })
      await userEvent.keyboard('{Tab}')
      expect(action).toHaveFocus()
      act(() => clock.advance(HOVERCARD.closeDelayMs + 100))
      expect(action).toHaveFocus()
      expect(screen.getByTestId('hovercard')).toBe(preview)
      expect(within(preview).queryByTestId('children-toggle')).toBeNull()
      expect(within(preview).queryByText('Preview child')).toBeNull()
      await userEvent.keyboard('{Enter}')
      expect(within(preview).getByRole('menu')).not.toBeNull()
      await userEvent.keyboard('{Escape}')
      expect(screen.queryByTestId('hovercard')).toBeNull()
      expect(tile).toHaveFocus()
      act(() => clock.advance(HOVERCARD.openDelayMs + 100))
      expect(screen.queryByTestId('hovercard')).toBeNull()
    } finally { view.unmount(); clock.uninstall() }
  })

  it(`preserves pointer hover delay, popup entry and leave at ${width}px`, async () => {
    await page.viewport(width, height)
    const clock = installHoverClock()
    useSessionStore.setState({ sessions: [makeSession({ id: 'pointer', title: 'Pointer preview' })] })
    const view = render(<MemoryRouter><SessionTiles /></MemoryRouter>)
    try {
      const tile = screen.getByRole('button', { name: /Pointer preview, / })
      await userEvent.hover(tile)
      act(() => clock.advance(HOVERCARD.openDelayMs - 1))
      expect(screen.queryByTestId('hovercard')).toBeNull()
      act(() => clock.advance(1))
      const preview = screen.getByTestId('hovercard')
      await userEvent.hover(preview)
      act(() => clock.advance(HOVERCARD.headingMaxMs + HOVERCARD.closeDelayMs + 100))
      expect(screen.getByTestId('hovercard')).toBe(preview)
      await userEvent.unhover(preview)
      expect(screen.queryByTestId('hovercard')).toBeNull()
    } finally { view.unmount(); clock.uninstall() }
  })
}

for (const width of browserWidths([390, 3840])) it(`shows the specific git-status refusal and retries at ${width}`, async () => {
  await page.viewport(width, 900)
  const base = apiFetch.getMockImplementation()!
  let refused = true
  apiFetch.mockImplementation(async (...args) => {
    if (String(args[0]).includes('op=status') && refused) return json({ result: { ok: false, launch_generation: 'launch', generation: 1, error_code: 'unavailable', status_reason: 'metadata_refused' }, members: [{ id: 'app', name: 'app', path: 'app' }], runtime: 'host' })
    return base(...args)
  })
  mount(width); await openExplorer()
  await userEvent.click(await screen.findByRole('treeitem', { name: /^app(?:,|$)/ }))
  const unavailable = await screen.findByText('Git status unavailable')
  expect(unavailable.parentElement).toHaveAttribute('title', expect.stringContaining('Git metadata failed its safety check'))
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  refused = false
  await userEvent.click(screen.getByRole('button', { name: 'Refresh workspace' }))
  await userEvent.click(await screen.findByRole('treeitem', { name: /^app(?:,|$)/ }))
  await screen.findByText('main · 4 changed')
  expect(screen.queryByText('Git status unavailable')).toBeNull()
  expect(screen.queryByTitle(/Git metadata failed its safety check/)).toBeNull()
})

for (const width of browserWidths([390, 1024, 3840])) {
  it(`opens a side chat as its normal view and closes only the tab at ${width}px`, async () => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    localStorage.removeItem('blerg-runner.session-dock.windows')
    useSessionStore.setState({ sessions: [...useSessionStore.getState().sessions, makeSession({ id: 'side', title: 'Review the change', parent_session_id: 'sess-a', kind: 'agent', status: 'running' })] })
    const transcript = useTranscriptStore.getState()
    transcript.ingest({ session_id: 'side', client_event_id: 'side-message', seq: 1, ts: '2026-10-30T12:00:00Z', kind: 'assistant_text', payload: { text: 'This is the focused review conversation.', done: true } } as AgentEvent)
    transcript.ingestReplayDone('side', { lastSeq: 1, hasMore: false })
    mount(width); await openExplorer()
    const main = screen.getByRole('treeitem', { name: 'Main' })
    expect(main).toBeVisible()
    await userEvent.click(screen.getByRole('treeitem', { name: 'Review the change' }))
    await waitFor(() => expect(screen.getByText('This is the focused review conversation.')).toBeVisible())
    expect(screen.getByRole('tab', { name: 'Review the change' })).toBeVisible()
    expect(screen.queryByRole('button', { name: 'Close Chat' })).toBeNull()
    if (width >= 900) {
      await userEvent.dblClick(screen.getByRole('treeitem', { name: 'Review the change' }))
      expect(screen.getByRole('tab', { name: 'Review the change' }).closest('.session-tab')).not.toHaveClass('preview')
      await userEvent.click(screen.getByRole('tab', { name: 'Files' }))
      await userEvent.click(await screen.findByRole('treeitem', { name: /README/ }))
      const transfer = new DataTransfer()
      fireEvent.dragStart(screen.getByRole('tab', { name: 'Review the change' }).parentElement!, { dataTransfer: transfer })
      const group = screen.getByTestId('session-groups').querySelector('[data-group-id]')!, rect = box(group)
      fireEvent.dragOver(group, { dataTransfer: transfer, clientX: rect.right - 4, clientY: rect.top + rect.height / 2 })
      fireEvent.drop(group, { dataTransfer: transfer, clientX: rect.right - 4, clientY: rect.top + rect.height / 2 })
      await waitFor(() => expect(screen.getByTestId('session-groups').querySelectorAll('[data-group-id]')).toHaveLength(2))
      expect(screen.getByText('This is the focused review conversation.')).toBeVisible()
      await userEvent.click(screen.getByRole('tab', { name: 'Channels' }))
    }
    if (width === 390) expect(screen.queryByRole('dialog', { name: 'Explorer' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Close Review the change' }))
    expect(screen.queryByRole('tab', { name: 'Review the change' })).toBeNull()
    expect(useSessionStore.getState().sessions.find(s => s.id === 'side')?.status).toBe('running')
    if (width === 390) await openExplorer()
    expect(screen.getByRole('treeitem', { name: 'Review the change' })).toBeVisible()
    noOverflow(width)
  })
}
for (const width of [1024, 3840]) {
  it(`compares Sessions and Files explorer rows side by side at ${width}px`, async () => {
    await page.viewport(width, 900)
    localStorage.removeItem('blerg-runner.session-dock.windows')
    useSessionStore.setState({ sessions: [makeSession({ id: 'sess-a', title: 'Coordinate' }), makeSession({ id: 'side', title: 'Review', parent_session_id: 'sess-a', status: 'running' }), makeSession({ id: 'deep', title: 'Nested review', parent_session_id: 'side' })] })
    render(<MemoryRouter><div style={{ display: 'flex', gap: 16, fontSize: 13, height: 600 }}><div style={{ width: 300, display: 'flex' }}><SessionDockWindows sessionId="sess-a" mobile={false} files={null} onOpen={() => {}} /></div><div style={{ width: 300 }}><WorkspaceTree sessionId="sess-a" active /></div></div></MemoryRouter>)
    const session = screen.getByRole('treeitem', { name: 'Review' })
    const file = await screen.findByRole('treeitem', { name: /README/ })
    for (const property of ['height', 'paddingTop', 'paddingBottom', 'paddingInlineStart', 'columnGap', 'fontSize']) expect(getComputedStyle(session)[property as keyof CSSStyleDeclaration]).toBe(getComputedStyle(file)[property as keyof CSSStyleDeclaration])
    expect(session.querySelector('.workspace-codes [data-testid="status-dot"]')).not.toBeNull()
    expect(session.querySelector('.workspace-chevron')?.textContent).toBe('▸')
    session.focus(); await userEvent.keyboard('{ArrowRight}')
    const nested = screen.getByRole('treeitem', { name: 'Nested review' })
    expect(nested).toHaveAttribute('aria-level', '2')
    expect(Math.round(box(nested).left - box(session).left)).toBe(17)
    await userEvent.keyboard('{ArrowRight}')
    expect(nested).toHaveFocus()
    await userEvent.keyboard('{ArrowLeft}')
    expect(session).toHaveFocus()
    await userEvent.click(session)
    await userEvent.click(file)
    expect(getComputedStyle(session).backgroundColor).toBe(getComputedStyle(file).backgroundColor)
    await userEvent.keyboard('{Escape}')
    if (shotDir) await page.screenshot({ path: `${shotDir}/sessions-files-${width}.png` })
  })
}
it('stacks and merges dock tools, keeps their split and disables dragging on phone', async () => {
  await page.viewport(1024, 900)
  localStorage.removeItem('blerg-runner.session-dock.windows')
  mount(1024); await openExplorer()
  const transfer = new DataTransfer()
  fireEvent.dragStart(screen.getByRole('tab', { name: 'Files' }), { dataTransfer: transfer })
  const panel = screen.getByTestId('dock-groups').querySelector('.session-group')!
  fireEvent.drop(panel, { dataTransfer: transfer, clientX: box(panel).left + box(panel).width / 2, clientY: box(panel).bottom - 10 })
  const divider = screen.getByRole('separator', { name: 'Resize tab groups 1' })
  expect(screen.getByRole('tree', { name: 'Channels' })).toBeVisible()
  await screen.findByRole('treeitem', { name: /README/ })
  expect(screen.getByRole('tree', { name: 'Workspace files' })).toBeVisible()
  divider.focus(); await userEvent.keyboard('{ArrowDown}')
  expect(divider).toHaveAttribute('aria-valuenow', '55')
  expect(JSON.parse(localStorage.getItem('blerg-runner.session-dock.windows.person-a.sess-a')!).layout.ratio).toBe(.55)
  fireEvent.dragStart(screen.getByRole('tab', { name: 'Files' }), { dataTransfer: transfer })
  fireEvent.drop(screen.getAllByRole('tablist', { name: 'Dock tools' })[0], { dataTransfer: transfer })
  expect(screen.queryByRole('separator', { name: 'Resize tab groups 1' })).toBeNull()
  await page.viewport(390, 844)
  expect(screen.getByRole('tab', { name: 'Files' })).toHaveAttribute('draggable', 'false')
  noOverflow(390)
})

it('opens a discarded chat History from Finished', async () => {
  await page.viewport(1024, 900)
  localStorage.removeItem('blerg-runner.session-dock.windows')
  useSessionStore.setState({ sessions: [...useSessionStore.getState().sessions, makeSession({ id: 'finished-side', title: 'Finished review', kind: 'agent', parent_session_id: 'sess-a', status: 'ended', archived_at: '2026-10-30T12:00:00Z' })] })
  mount(1024); await openExplorer()
  expect(screen.queryByRole('treeitem', { name: 'Finished review' })).toBeNull()
  await userEvent.click(screen.getByRole('treeitem', { name: 'Finished' }))
  expect(screen.getByRole('treeitem', { name: 'Finished review' })).toBeVisible()
  await userEvent.click(screen.getByRole('link', { name: 'History' }))
  await waitFor(() => expect(screen.getByRole('tab', { name: 'History' })).toHaveAttribute('aria-selected', 'true'))
})
// Review regressions use restored tabs so DOM order differs from session identity.
function reviewTabs(active = 'chat', split = false) {
  localStorage.removeItem('blerg-runner.session-dock.windows')
  useSessionStore.setState({ sessions: [...useSessionStore.getState().sessions, makeSession({ id: 'side', title: '', parent_session_id: 'sess-a', kind: 'agent', status: 'idle' })] })
  localStorage.setItem('blerg-runner.tabs.person-a.sess-a', JSON.stringify({ tabs: [{ id: 'session-chat:side', view: { kind: 'session-chat', sessionId: 'side', label: 'Untitled chat' }, preview: false }], active, focused: split ? 'left' : 'main', layout: split ? { type: 'split', id: 'split', axis: 'vertical', ratio: .5, first: { type: 'group', id: 'left', tabs: ['session-chat:side'], active: 'session-chat:side' }, second: { type: 'group', id: 'right', tabs: ['chat'], active: 'chat' } } : { type: 'group', id: 'main', tabs: ['chat', 'session-chat:side'], active } }))
  const transcript = useTranscriptStore.getState()
  transcript.ingest({ session_id: 'side', client_event_id: 'review-side', seq: 1, ts: '2026-10-30T12:00:00Z', kind: 'assistant_text', payload: { text: 'Focused review transcript', done: true } } as AgentEvent)
  transcript.ingestReplayDone('side', { lastSeq: 1, hasMore: false })
}
for (const width of [1440, 1920]) {
  it(`review: History is consumed once and can stay closed at ${width}px`, async () => {
    await page.viewport(width, 900)
    let renders = 0
    const writes = vi.spyOn(Storage.prototype, 'setItem')
    await act(async () => { render(<MemoryRouter initialEntries={['/sessions/sess-a?panel=history']}><CommandPaletteProvider><RouteSearch /><div style={{ height: '100vh' }}><Profiler id="history-review" onRender={() => { if (++renders > 80) throw new Error('History request rendered more than 80 times') }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></Profiler></div></CommandPaletteProvider></MemoryRouter>) })
    await waitFor(() => expect(screen.getByTestId('route-search').textContent).toBe(''))
    await railReady()
    vi.useFakeTimers({toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval']})
    try {
      await act(async () => { fireEvent.click(screen.getByTestId('rail-toggle')) })
      // Flush the layout observer's React update before measuring steady-state renders.
      await act(async () => { await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))) })
      const renderCount = renders
      const count = writes.mock.calls.filter(c => c[0].includes('sessionRail')).length
      const reads = apiFetch.mock.calls.filter(c => String(c[0]).includes('/live')).length
      await act(async () => { vi.advanceTimersByTime(350) })
      expect(writes.mock.calls.filter(c => c[0].includes('sessionRail')).length).toBe(count)
      expect(renders).toBe(renderCount)
      expect(apiFetch.mock.calls.filter(c => String(c[0]).includes('/live')).length - reads).toBeLessThanOrEqual(1)
      expect(screen.getByTestId('rail-toggle')).toHaveAttribute('aria-expanded', 'false')
      writes.mockRestore()
    } finally { vi.useRealTimers() }
  })
  it(`review: selects each split composer by session identity at ${width}px`, async () => {
    await page.viewport(width, 900); reviewTabs('session-chat:side', true); mount(width)
    await userEvent.click(screen.getByRole('tab', { name: 'Main' }))
    const main = document.querySelector<HTMLTextAreaElement>('[data-view-id="chat"] .agent-composer textarea')!
    await waitFor(() => expect(main).toHaveFocus())
    await userEvent.click(screen.getByRole('tab', { name: 'Untitled chat' }))
    const side = document.querySelector<HTMLTextAreaElement>('[data-view-id="session-chat:side"] .agent-composer textarea')!
    await waitFor(() => expect(side).toHaveFocus())
  })
}
it('review: hidden side chats do not register model or effort commands', async () => {
  await page.viewport(1440, 900); reviewTabs(); mount(1440)
  await userEvent.click(screen.getByRole('button', { name: 'Search commands and files' }))
  await userEvent.fill(screen.getByRole('combobox', { name: /^Search commands/ }), '>Set effort: low')
  expect(screen.getAllByRole('option', { name: 'Set effort: low' })).toHaveLength(1)
})
it('review: renamed sessions update restored tabs and palette labels', async () => {
  await page.viewport(1440, 900); reviewTabs(); mount(1440)
  act(() => useSessionStore.setState({ sessions: useSessionStore.getState().sessions.map(s => s.id === 'side' ? { ...s, title: 'Renamed review' } : s) }))
  expect(screen.getByRole('tab', { name: 'Renamed review' })).toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: 'Search commands and files' }))
  await userEvent.fill(screen.getByRole('combobox', { name: /^Search commands/ }), '>Go to tab 2: Renamed review')
  expect(screen.getByRole('option', { name: /^Go to tab 2: Renamed review/ })).toBeVisible()
})
it('review: a hidden side chat signals new output and clears it on selection', async () => {
  await page.viewport(1440, 900); reviewTabs(); mount(1440)
  await screen.findByText('Focused review transcript')
  act(() => useTranscriptStore.getState().ingest({ session_id: 'side', client_event_id: 'review-new', seq: 2, ts: '2026-10-30T12:00:01Z', kind: 'assistant_text', payload: { text: 'New hidden output', done: true } } as AgentEvent))
  await waitFor(() => expect(screen.getByLabelText('New output in Untitled chat')).toBeVisible())
  await userEvent.click(screen.getByRole('tab', { name: /Untitled chat/ }))
  await waitFor(() => expect(screen.queryByLabelText('New output in Untitled chat')).toBeNull())
})
it('review: phone Sessions focuses inside the sheet, traps History and closes on navigation', async () => {
  await page.viewport(390, 844)
  localStorage.removeItem('blerg-runner.session-dock.windows')
  useSessionStore.setState({ sessions: [...useSessionStore.getState().sessions, makeSession({ id: 'done-side', title: 'Done', kind: 'agent', parent_session_id: 'sess-a', status: 'ended', archived_at: '2026-10-30T12:00:00Z' })] })
  mount(390); await openExplorer()
  const sheet = screen.getByRole('dialog', { name: 'Explorer' })
  expect(sheet.contains(document.activeElement)).toBe(true)
  await userEvent.click(screen.getByRole('treeitem', { name: 'Finished' }))
  const link = screen.getByRole('link', { name: 'History' }); link.focus()
  await userEvent.keyboard('{Tab}')
  expect(document.activeElement).toBe(within(sheet).getByRole('button', { name: 'Collapse explorer' }))
  await userEvent.click(link)
  await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Explorer' })).toBeNull())
})
it('review: side chats do not consume the route artifact link', async () => {
  const previousUrl = location.href
  history.replaceState(history.state, '', location.pathname + '?artifact=report')
  await page.viewport(1440, 900); reviewTabs()
  const reads: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string) => { reads.push(String(url)); return json(String(url).endsWith('/artifacts') ? { artifacts: [{ id: 'report', name: 'report.md', size: 4, view: 'markdown', content_type: 'text/markdown', version: 1 }] } : []) }))
  render(<MemoryRouter initialEntries={['/sessions/sess-a?artifact=report']}><CommandPaletteProvider><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></CommandPaletteProvider></MemoryRouter>)
  await new Promise(resolve => setTimeout(resolve, 150))
  expect(reads.some(url => url.includes('/side/artifacts/report'))).toBe(false)
  history.replaceState(history.state, '', previousUrl)
})
async function reviewPhoneHistory() {
  await page.viewport(390, 844)
  localStorage.removeItem('blerg-runner.session-dock.windows')
  useSessionStore.setState({ sessions: [...useSessionStore.getState().sessions, makeSession({ id: 'done-phone', title: 'Done phone', kind: 'agent', parent_session_id: 'sess-a', status: 'ended', archived_at: '2026-10-30T12:00:00Z' })] })
  mount(390); await openExplorer()
  await userEvent.click(screen.getByRole('treeitem', { name: 'Finished' }))
}
it('review: phone History is the final trapped focus stop', async () => {
  await reviewPhoneHistory()
  const sheet = screen.getByRole('dialog', { name: 'Explorer' })
  screen.getByRole('link', { name: 'History' }).focus()
  await userEvent.keyboard('{Tab}')
  expect(document.activeElement).toBe(within(sheet).getByRole('button', { name: 'Collapse explorer' }))
})
it('review: phone History navigation dismisses the sheet', async () => {
  await reviewPhoneHistory()
  await userEvent.click(screen.getByRole('link', { name: 'History' }))
  await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Explorer' })).toBeNull())
  await waitFor(() => expect(screen.getByTestId('route-search').textContent).toBe(''))
})

it('review: command returns to Main when a child composer is earlier in the DOM', async () => {
  await page.viewport(1920, 900); reviewTabs('session-chat:side', true); mount(1920)
  const main = document.querySelector<HTMLTextAreaElement>('[data-view-id="chat"] .agent-composer textarea')!
  const side = document.querySelector<HTMLTextAreaElement>('[data-view-id="session-chat:side"] .agent-composer textarea')!
  const sideDraft = side.value
  side.focus()
  act(() => { reviewCommands.registry!.getSnapshot().items.find(command => command.id === 'tab-chat')!.run() })
  await waitFor(() => expect(main).toHaveFocus())
  await userEvent.fill(main, '')
  await userEvent.keyboard('Main draft')
  expect(main.value).toBe('Main draft')
  expect(side.value).toBe(sideDraft)
})
it('review: activating a hidden side tab focuses its own composer', async () => {
  await page.viewport(1440, 900); reviewTabs(); mount(1440)
  const side = document.querySelector<HTMLTextAreaElement>('[data-view-id="session-chat:side"] .agent-composer textarea')!
  const mainDraft = document.querySelector<HTMLTextAreaElement>('[data-view-id="chat"] .agent-composer textarea')!.value
  await userEvent.click(screen.getByRole('tab', { name: 'Untitled chat' }))
  await waitFor(() => expect(side).toHaveFocus())
  await userEvent.fill(side, '')
  await userEvent.keyboard('Side draft')
  expect(side.value).toBe('Side draft')
  expect(document.querySelector<HTMLTextAreaElement>('[data-view-id="chat"] .agent-composer textarea')!.value).toBe(mainDraft)
})
for (const width of browserWidths([390, 1024, 3840])) it(`review: merged tools retain session tree state and scroll at ${width}px`, async () => {
  await page.viewport(width, width === 390 ? 844 : 900)
  localStorage.removeItem('blerg-runner.session-dock.windows')
  useSessionStore.setState({ sessions: [...useSessionStore.getState().sessions,
    makeSession({ id: 'branch', title: 'Branch', parent_session_id: 'sess-a' }),
    makeSession({ id: 'nested', title: 'Nested', parent_session_id: 'branch' }),
    ...Array.from({ length: 60 }, (_, i) => makeSession({ id: `leaf-${i}`, title: `Leaf ${i}`, parent_session_id: 'sess-a' })),
    makeSession({ id: 'old', title: 'Finished old', parent_session_id: 'sess-a', status: 'ended', archived_at: '2026-10-30T12:00:00Z' }),
  ] })
  mount(width); await openExplorer()
  fireEvent.keyDown(screen.getByRole('treeitem', { name: 'Branch' }), { key: 'ArrowRight' })
  fireEvent.click(screen.getByRole('treeitem', { name: 'Finished' }))
  const panel = screen.getByRole('tabpanel', { name: 'Channels' })
  panel.scrollTop = 120; expect(panel.scrollTop).toBe(120)
  await userEvent.click(screen.getByRole('tab', { name: 'Files' }))
  await userEvent.click(screen.getByRole('tab', { name: 'Channels' }))
  expect(screen.getByRole('tabpanel', { name: 'Channels' })).toBe(panel)
  expect(panel.scrollTop).toBe(120)
  expect(screen.getByRole('treeitem', { name: 'Branch' })).toHaveAttribute('aria-expanded', 'true')
  expect(screen.getByRole('treeitem', { name: 'Finished' })).toHaveAttribute('aria-expanded', 'true')
  expect(screen.getByRole('treeitem', { name: 'Nested' })).toBeVisible()
  expect(screen.getByRole('treeitem', { name: 'Finished old' })).toBeVisible()
})
it('review: a History URL waits for the session before consuming the request', async () => {
  await page.viewport(1440, 900)
  useSessionStore.setState({ sessions: [] })
  mount(1440, '/sessions/sess-a?panel=history')
  await new Promise(resolve => setTimeout(resolve, 80))
  act(() => useSessionStore.setState({ sessions: [makeSession({ id: 'sess-a', kind: 'agent', status: 'idle' })] }))
  await waitFor(() => expect(screen.getByRole('tab', { name: 'History' })).toHaveAttribute('aria-selected', 'true'))
  await waitFor(() => expect(screen.getByTestId('route-search').textContent).toBe(''))
})

for (const source of ['dock', 'restored tab']) it(`final review: phone ${source} activation leaves the keyboard closed`, async () => {
  await page.viewport(390, 844); reviewTabs(); mount(390)
  if (source === 'dock') {
    await openExplorer()
    await userEvent.click(screen.getByRole('treeitem', { name: 'Untitled channel' }))
  } else {
    await userEvent.click(screen.getByRole('tab', { name: 'Untitled chat' }))
  }
  await waitFor(() => expect(document.querySelector('[data-view-id="session-chat:side"]')).toBeVisible())
  await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))
  expect(document.activeElement?.matches('.agent-composer textarea')).toBe(false)
})
it('final review: keyboard can reach the other split tab strip without entering its composer', async () => {
  await page.viewport(1440, 900); reviewTabs('session-chat:side', true); mount(1440)
  const main = screen.getByRole('tab', { name: 'Main' })
  const side = screen.getByRole('tab', { name: 'Untitled chat' })
  await userEvent.click(main)
  // The Explorer toggle precedes the other group's tab and does not activate that group.
  act(() => toggle().focus())
  await userEvent.keyboard('{Tab}')
  await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))
  expect(side).toHaveFocus()
  await userEvent.keyboard('{Enter}')
  await waitFor(() => expect(document.querySelector('[data-view-id="session-chat:side"] .agent-composer textarea')).toHaveFocus())
})
it('final review: restored missing chats are marked read only after their session arrives', async () => {
  // Both chats are visible; the URL selects Main without hiding the restored side pane.
  await page.viewport(1440, 900); reviewTabs('chat', true)
  const side = useSessionStore.getState().sessions.find(s => s.id === 'side')!
  useSessionStore.setState({ sessions: useSessionStore.getState().sessions.filter(s => s.id !== 'side') })
  send.mockClear(); mount(1440)
  await screen.findByText('This workspace is unavailable.')
  const reads = () => send.mock.calls.filter(([message]) => message.type === 'mark_session_read' && message.session_id === 'side')
  expect(reads()).toHaveLength(0)
  act(() => useSessionStore.setState({ sessions: [...useSessionStore.getState().sessions, side] }))
  await waitFor(() => expect(reads()).toHaveLength(1))
})

for (const width of [1024, 3840]) for (const stacked of [false, true]) {
  it(`switches dock tools without collapse controls at ${width}px (${stacked ? 'stacked' : 'merged'})`, async () => {
    await page.viewport(width, 900)
    localStorage.setItem('blerg-runner.session-dock.windows', JSON.stringify({ active: 'sessions', panels: stacked ? ['sessions', 'files'] : [], ratio: .5 }))
    mount(width); await openExplorer()
    const dock = screen.getByRole('region', { name: 'Explorer' })
    expect(dock.querySelector('.session-dock-collapse')).toBeNull()
    expect(within(dock).queryByRole('button', { name: 'Collapse explorer' })).toBeNull()
    const row = within(dock).getAllByRole('tablist', { name: 'Dock tools' })[0]
    expect(getComputedStyle(row.closest('.session-tabbar')!).paddingInlineEnd).toBe('6px')
    await userEvent.click(within(row).getByRole('tab', { name: 'Repositories' }))
    expect(within(dock).getByRole('tabpanel', { name: 'Repositories' })).toBeVisible()
    if (shotDir && width === 1024 && !stacked) await page.screenshot({ path: `${shotDir}/dock-controls-1024.png` })
    await userEvent.click(toggle())
    expect(screen.queryByRole('region', { name: 'Explorer' })).toBeNull()
    expect(toggle()).toHaveAttribute('aria-expanded', 'false')
  })
}
for (const stacked of [false, true]) it(`keeps phone dock collapse controls in the sheet header (${stacked ? 'saved stack' : 'merged'})`, async () => {
  await page.viewport(390, 844)
  localStorage.setItem('blerg-runner.session-dock.windows', JSON.stringify({ active: 'sessions', panels: stacked ? ['sessions', 'files'] : [], ratio: .5 }))
  mount(390); await openExplorer()
  const sheet = screen.getByRole('dialog', { name: 'Explorer' })
  expect(sheet.querySelector('.session-dock-collapse')).toBeNull()
  const header = sheet.querySelector('.explorer-sheet-header')!, row = within(sheet).getByRole('tablist', { name: 'Dock tools' })
  expect(box(row).top).toBeGreaterThanOrEqual(box(header).bottom)
  expect(getComputedStyle(row.closest('.session-tabbar')!).paddingRight).toBe('6px')
  for (const name of ['Channels', 'Files', 'Repositories']) expect(within(sheet).getByRole('tab', { name })).toBeVisible()
  await userEvent.click(within(sheet).getByRole('tab', { name: 'Repositories' }))
  expect(within(sheet).getByRole('tabpanel', { name: 'Repositories' })).toBeVisible()
  if (shotDir && !stacked) await page.screenshot({ path: `${shotDir}/dock-controls-390.png` })
  await userEvent.click(within(sheet).getByRole('button', { name: 'Collapse explorer' }))
  expect(screen.queryByRole('dialog', { name: 'Explorer' })).toBeNull()
  noOverflow(390)
})

for (const width of browserWidths([390, 1024, 3840])) it(`previews dock chats, keeps explicit interactions and shares role icons at ${width}px`, async () => {
  await page.viewport(width, 900)
  localStorage.removeItem('blerg-runner.session-dock.windows')
  const chats = [makeSession({ id: 'discuss', title: 'Discuss design', parent_session_id: 'sess-a', kind: 'agent', status: 'idle' }), makeSession({ id: 'plan', title: 'Plan delivery', parent_session_id: 'sess-a', kind: 'agent', status: 'idle' }), makeSession({ id: 'reviewer', title: 'Review code', parent_session_id: 'sess-a', started_by: { kind: 'session' }, kind: 'agent', status: 'idle' })]
  useSessionStore.setState({ sessions: [...useSessionStore.getState().sessions, ...chats] })
  for (const chat of chats) useTranscriptStore.getState().ingestReplayDone(chat.id, { lastSeq: 0, hasMore: false })
  send.mockReturnValue(true)
  mount(width); await openExplorer()
  await userEvent.click(screen.getByRole('treeitem', { name: 'Discuss design' }))
  const discussion = screen.getByRole('tab', { name: 'Discuss design' })
  expect(discussion.querySelector('[data-chat-role="channel"] path')?.getAttribute('d')).toBe('M3 2h10v8H6l-3 3z')
  expect(discussion.closest('.session-tab')?.classList.contains('preview')).toBe(width !== 390)
  if (width === 390) await openExplorer()
  await userEvent.click(screen.getByRole('treeitem', { name: 'Workers, 1' }))
  const worker = screen.getByRole('treeitem', { name: 'Review code' })
  expect(worker.querySelector('[data-chat-role="worker"]')).toBeNull()
  await userEvent.click(worker)
  const review = screen.getByRole('tab', { name: 'Review code' })
  expect(review.querySelector('[data-chat-role="worker"]')).toHaveTextContent('◇')
  expect(review.closest('.session-tab')?.classList.contains('preview')).toBe(width !== 390)
  if (width === 390) {
    expect(screen.getByRole('tab', { name: 'Discuss design' })).toBeVisible()
    expect(screen.queryByRole('dialog', { name: 'Explorer' })).toBeNull()
    expect(document.activeElement?.matches('.agent-composer textarea')).toBe(false)
    return
  }
  expect(screen.queryByRole('tab', { name: 'Discuss design' })).toBeNull()
  await userEvent.dblClick(worker)
  expect(screen.getByRole('tab', { name: 'Review code' }).closest('.session-tab')).not.toHaveClass('preview')
  const channel = screen.getByRole('treeitem', { name: 'Plan delivery' })
  channel.focus(); await userEvent.keyboard('{Enter}')
  const plan = screen.getByRole('tab', { name: 'Plan delivery' })
  expect(plan.closest('.session-tab')).toHaveClass('preview')
  expect(getComputedStyle(plan.querySelector(':scope > span:not([data-chat-role])')!).fontStyle).toBe('italic')
  const composer = document.querySelector<HTMLTextAreaElement>('[data-view-id="session-chat:plan"] .agent-composer textarea')!
  await userEvent.fill(composer, 'Keep this conversation open')
  await userEvent.keyboard('{Enter}')
  await waitFor(() => expect(screen.getByRole('tab', { name: 'Plan delivery' }).closest('.session-tab')).not.toHaveClass('preview'))
  await userEvent.click(screen.getByRole('treeitem', { name: 'Discuss design' }))
  expect(screen.getByRole('tab', { name: 'Plan delivery' })).toBeVisible()
  expect(screen.getByRole('tab', { name: 'Review code' })).toBeVisible()
  expect(screen.getByRole('tab', { name: 'Discuss design' }).closest('.session-tab')).toHaveClass('preview')
})
