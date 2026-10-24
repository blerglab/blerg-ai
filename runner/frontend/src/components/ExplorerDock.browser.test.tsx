import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { cdp, page, userEvent } from 'vitest/browser'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import SessionList from './SessionList'
import CommandPaletteProvider from './CommandPalette'
import { useSessionStore } from '../hooks/useSessionStore'
import { explorerPrefKey } from '../hooks/useExplorerLayout'
import { makeDaemon, makeSession } from '../test/fixtures'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'
import { invalidateChatTransport } from '../lib/chatTransport'
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
  document.documentElement.setAttribute('data-theme', 'dark')
  useTranscriptStore.setState({ sessions: {} })
  const transcript = useTranscriptStore.getState()
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'dock-user', seq: 1, ts: '2026-10-24T12:00:00Z', kind: 'user_message', payload: { text: 'Add the explorer beside the chat and keep the published files in the panel.', source: 'chat' } } as AgentEvent)
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'dock-assistant', seq: 2, ts: '2026-10-24T12:00:01Z', kind: 'assistant_text', payload: { text: 'The workspace tree is available from the folder control in the header. You can resize it with the divider, browse repositories with the keyboard, and open files in the shared viewer.', done: true } } as AgentEvent)
  transcript.ingestReplayDone('sess-a', { lastSeq: 2, hasMore: false })
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ kind: 'agent', status: 'idle', title: 'Build the workspace explorer' })] })
  localStorage.setItem(RAIL_PREF_KEY, '1')
  localStorage.setItem(RAIL_TAB_KEY, 'files')
  apiFetch.mockReset().mockImplementation(async (url: string) => {
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
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals(); document.documentElement.removeAttribute('data-theme') })
function mount(width: number) {
  return render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider>
    <div data-testid="dock-layout" style={{ display: 'flex', height: '100vh' }}>
      {width >= 900 && <SessionList variant="sidebar" onNewSession={() => {}} />}
      <div style={{ flex: 1, minWidth: 0 }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div>
    </div>
  </CommandPaletteProvider></MemoryRouter>)
}
const toggle = () => screen.getByRole('button', { name: 'Toggle explorer' })
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
    await userEvent.click(toggle())
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
    expect(box(sidebar).right).toBe(box(dock).left)
    expect(document.activeElement).toBe(screen.getByRole('combobox', { name: 'Search workspace files' }))
    const handle = screen.getByRole('separator', { name: 'Explorer width' })
    // Real pointer drag (captured by the divider), beyond the old 340 px panel width.
    const start = box(handle)
    await userEvent.dragAndDrop(handle, chat(), { sourcePosition: { x: start.width / 2, y: 150 }, targetPosition: { x: 240, y: 150 } })
    await waitFor(() => expect(box(dock).width).toBeGreaterThan(initialWidth + 150))
    expect(geometry(panel)).toEqual(beforePanel)
    expect(box(dock).right).toBe(box(chat()).left)
    handle.focus()
    await userEvent.keyboard('{End}')
    expect(box(dock).width).toBe(Number(handle.getAttribute('aria-valuemax')))
    if (width === 3840) expect(box(dock).width).toBe(1536)
    expect(box(chat()).width).toBeGreaterThan(200)
    await userEvent.keyboard('{Home}{ArrowRight}')
    const savedWidth = box(dock).width
    const key = explorerPrefKey('person-a')
    expect(JSON.parse(localStorage.getItem(key)!)).toEqual({ open: true, width: savedWidth })
    view.unmount(); mount(width)
    const restored = screen.getByRole('region', { name: 'Explorer' })
    expect(box(restored).width).toBe(savedWidth)
    expect(toggle().getAttribute('aria-expanded')).toBe('true')
    expect(localStorage.getItem(RAIL_PREF_KEY)).toBe('1')
    expect(box(screen.getByTestId('session-rail')).width).toBe(beforePanel[2])
    noOverflow(width)
    await userEvent.click(screen.getByRole('button', { name: 'Collapse explorer' }))
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
    expect(toggle().getAttribute('aria-expanded')).toBe('false')
    await userEvent.click(toggle())
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
    expect(toggle().getAttribute('aria-expanded')).toBe('true')
    await userEvent.click(screen.getByRole('button', { name: 'Collapse explorer' }))
    expect(toggle().getAttribute('aria-expanded')).toBe('false')
    expect(document.activeElement).toBe(toggle())
    await userEvent.click(screen.getByRole('button', { name: 'Search commands and files' }))
    const palette = screen.getByRole('dialog', { name: 'Command palette' })
    await userEvent.click(within(palette).getByRole('option', { name: /Toggle explorer/ }))
    await screen.findByRole('tree')
    expect(screen.queryByRole('dialog', { name: 'Command palette' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Collapse explorer' }))
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
  await userEvent.click(toggle())
  const sheet = screen.getByRole('dialog', { name: 'Explorer' })
  const close = within(sheet).getByRole('button', { name: 'Collapse explorer' })
  expect(geometry(sheet)).toEqual([0, 0, 390, 844])
  expect(geometry(chat())).toEqual(beforeChat)
  expect(document.activeElement).toBe(screen.getByRole('combobox', { name: 'Search workspace files' }))
  expect(document.querySelector<HTMLElement>('[data-session-root]')!.inert).toBe(true)
  expect(screen.queryByRole('separator')).toBeNull()
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
  const viewer = await screen.findByTestId('workspace-viewer-overlay')
  await screen.findByText('Workspace viewer fixture')
  const viewerClose = within(viewer).getByRole('button', { name: /^Close$/ })
  const point = box(viewerClose)
  expect(viewer.contains(document.elementFromPoint(point.x + point.width / 2, point.y + point.height / 2))).toBe(true)
  await userEvent.click(viewerClose)
  expect(screen.queryByTestId('workspace-viewer-overlay')).toBeNull()
  expect(screen.getByRole('dialog', { name: 'Explorer' })).toBeTruthy()
  expect(document.activeElement).toBe(file)
  await userEvent.keyboard('{Escape}')
  expect(screen.queryByRole('dialog', { name: 'Explorer' })).toBeNull()
  expect(document.activeElement).toBe(toggle())
  expect(document.querySelector<HTMLElement>('[data-session-root]')!.inert).toBe(false)
  expect(geometry(chat())).toEqual(beforeChat)
  expect(localStorage.getItem(RAIL_PREF_KEY)).toBe('1')
  view.unmount(); mount(390)
  expect(toggle().getAttribute('aria-expanded')).toBe('false')
  await userEvent.click(toggle())
  expect(geometry(screen.getByRole('dialog', { name: 'Explorer' }))).toEqual([0, 0, 390, 844])
})

it('retains the desktop width through a phone sheet and keeps the open phone preference across remounts', async () => {
  await page.viewport(3840, 2160)
  const first = mount(3840)
  await userEvent.click(toggle())
  const divider = screen.getByRole('separator')
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
    await userEvent.click(toggle())
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
    expect(added.querySelector('svg.artifact-glyph path')?.getAttribute('d')).toBe('M3.5 1.5h6L13 5v9.5H3.5zM9.5 1.5V5H13')
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
    const viewer = await screen.findByTestId('workspace-viewer-overlay')
    await screen.findByText('Workspace viewer fixture')
    const closeViewer = within(viewer).getByRole('button', { name: /^Close$/ })
    const closeBox = box(closeViewer)
    expect(viewer.contains(document.elementFromPoint(closeBox.x + closeBox.width / 2, closeBox.y + closeBox.height / 2))).toBe(true)
    await userEvent.click(closeViewer)
    expect(document.activeElement).toBe(refreshedFile)
    expect(explorer.querySelector('h2')).toBeNull()
    expect(getComputedStyle(explorer).backgroundColor).toBe(getComputedStyle(document.querySelector('[data-session-root]')!).backgroundColor)
    noOverflow(width)
  })
  it(`leaves editable inputs and browser Ctrl/Command+Shift+E unclaimed at ${width}`, async () => {
    await page.viewport(width, height); mount(width)
    await userEvent.click(toggle()); await screen.findByRole('tree')
    const input = screen.getByRole('combobox', { name: 'Search workspace files' }); input.focus()
    await userEvent.keyboard('{Control>}{Shift>}e{/Shift}{/Control}{Meta>}{Shift>}e{/Shift}{/Meta}')
    expect(document.activeElement).toBe(input)
    expect(toggle().getAttribute('aria-expanded')).toBe('true')
    for (const tag of ['textarea', 'div']) {
      const editable = document.createElement(tag)
      if (tag === 'div') { editable.contentEditable = 'true'; editable.setAttribute('role', 'textbox') }
      input.parentElement!.append(editable); editable.focus()
      const event = new KeyboardEvent('keydown', { key: 'E', ctrlKey: true, shiftKey: true, bubbles: true, cancelable: true })
      editable.dispatchEvent(event)
      expect(event.defaultPrevented).toBe(false)
      expect(document.activeElement).toBe(editable)
      expect(toggle().getAttribute('aria-expanded')).toBe('true')
      editable.remove()
    }
  })
}

it('reserves usable chat at 1024px, including saved widths, panel choices and viewport changes', async () => {
  await page.viewport(3840, 900); mount(3840)
  await userEvent.click(toggle()); screen.getByRole('separator').focus(); await userEvent.keyboard('{End}')
  expect(box(screen.getByRole('region', { name: 'Explorer' })).width).toBe(1536)
  await page.viewport(1024, 900)
  await waitFor(() => expect(box(chat()).width).toBeGreaterThanOrEqual(320))
  const columns = screen.getByTestId('session-columns')
  expect(columns.scrollWidth).toBeGreaterThan(columns.clientWidth)
  expect(getComputedStyle(columns).overflowX).toBe('auto')
  expect(box(screen.getByTestId('session-rail')).width).toBeGreaterThanOrEqual(340)
  noOverflow(1024)
  await userEvent.click(screen.getByTestId('rail-toggle'))
  screen.getByRole('separator').focus(); await userEvent.keyboard('{End}')
  expect(box(chat()).width).toBeGreaterThanOrEqual(320)
  noOverflow(1024)
  await userEvent.click(screen.getByTestId('rail-toggle'))
  expect(box(chat()).width).toBeGreaterThanOrEqual(320)
  await page.viewport(3840, 900)
  screen.getByRole('separator').focus(); await userEvent.keyboard('{End}')
  await waitFor(() => expect(box(screen.getByRole('region', { name: 'Explorer' })).width).toBe(1536))
})

it('collapses by dragging to the left edge and reopens from the header at 3840', async () => {
  await page.viewport(3840, 2160); mount(3840)
  await userEvent.click(toggle())
  const dock = screen.getByRole('region', { name: 'Explorer' })
  const handle = screen.getByRole('separator')
  await userEvent.dragAndDrop(handle, dock, { sourcePosition: { x: 4, y: 100 }, targetPosition: { x: 10, y: 100 } })
  expect(screen.queryByRole('region', { name: 'Explorer' })).toBeNull()
  expect(document.activeElement).toBe(toggle())
  await userEvent.click(toggle())
  expect(screen.getByRole('region', { name: 'Explorer' })).toBeTruthy()
})

for (const [width, height] of [[390, 844], [3840, 2160]]) it(`keeps the real terminal in charge of its keys at ${width}`, async () => {
  await page.viewport(width, height)
  useSessionStore.setState({ sessions: [makeSession({ kind: 'terminal', status: 'running' })] })
  mount(width)
  const terminalInput = document.querySelector<HTMLTextAreaElement>('textarea')!
  expect(terminalInput).toBeTruthy(); terminalInput.focus()
  await userEvent.keyboard('x{Control>}{Shift>}e{/Shift}{/Control}y')
  expect(document.activeElement).toBe(terminalInput)
  expect(toggle().getAttribute('aria-expanded')).toBe('false')
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
    await page.viewport(width, height); mount(width); await userEvent.click(toggle())
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
    const leafName = 'a-very-long-file-name-'.repeat(18) + 'leaf.txt'
    apiFetch.mockImplementation(async (url: string) => {
      if (!url.includes('/workspace?')) return json({})
      const path = new URL(url, 'https://example.com').searchParams.get('path') ?? ''
      const depth = path ? path.split('/').length : 0
      const name = depth === 28 ? leafName : `level-${depth}`
      return json({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: [{ name, path: path ? `${path}/${name}` : name, directory: depth < 28 }], truncated: false }, members: [], runtime: 'host' })
    })
    mount(width); await userEvent.click(toggle())
    if (width === 3840) { screen.getByRole('separator').focus(); await userEvent.keyboard('{Home}') }
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
    await page.viewport(width, height); mount(width); await userEvent.click(toggle())
    await screen.findByRole('treeitem', { name: 'README.md' })
    const input = screen.getByRole('combobox', { name: 'Search workspace files' })
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
    expect(document.activeElement).toBe(input)
    expect(input.getAttribute('aria-expanded')).toBe('false')
    await userEvent.keyboard('{ArrowUp}')
    expect(input.getAttribute('aria-expanded')).toBe('true')
    expect(document.getElementById(input.getAttribute('aria-activedescendant')!)?.getAttribute('role')).toBe('option')
    await userEvent.keyboard('{Escape}')
    expect(input.getAttribute('aria-expanded')).toBe('false')
    expect(input.hasAttribute('aria-activedescendant')).toBe(false)
    expect(document.activeElement).toBe(input)
    expect(toggle().getAttribute('aria-expanded')).toBe('true')
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
    await userEvent.click(toggle())
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
    await page.viewport(width, height)
    apiFetch.mockImplementation(async (url: string) => {
      if (!url.includes('/workspace?')) return json({})
      const path = new URL(url, 'https://example.com').searchParams.get('path') ?? ''
      const depth = path ? path.split('/').length : 0
      const name = depth === 28 ? 'leaf.txt' : `folder${depth}`
      return json({ result: { ok: true, launch_generation: 'launch', generation: 1,
        entries: [{ name, path: path ? `${path}/${name}` : name, directory: depth < 28 }], truncated: false }, members: [], runtime: 'host' })
    })
    mount(width)
    await userEvent.click(toggle())
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
    expect(document.activeElement).toBe(restoredLeaf)
    noOverflow(width)
  })
}
