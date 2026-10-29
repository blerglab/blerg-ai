import { useEffect } from 'react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent, cdp } from 'vitest/browser'
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import SessionList from './SessionList'
import RunnerCommands from './RunnerCommands'
import CommandPaletteProvider from './CommandPalette'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { sessionTabsKey } from '../hooks/useSessionTabs'
import { RAIL_PREF_KEY } from '../lib/sessionRail'
import { invalidateChatTransport } from '../lib/chatTransport'
import { openSessionFile } from '../lib/openSessionFile'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
vi.mock('../shellTransport', async original => ({ ...await original<typeof import('../shellTransport')>(), restoreShells: async () => [] }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../lib/engineModels', async original => ({ ...await original<typeof import('../lib/engineModels')>(), useEngineModels: () => ({ models: [], loading: false, fallback: false, source: 'live', engineEfforts: [] }) }))
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
const key = sessionTabsKey('person-a', 'sess-a')
const paths = ['main.ts', 'notes.md', 'tests.ts']
const shotDir = import.meta.env.VITE_FILE_TAB_SHOTS_DIR
const persisted = () => JSON.parse(localStorage.getItem(key)!)
const groupNodes = () => [...document.querySelectorAll<HTMLElement>('.session-group')]
const composer = () => document.querySelector<HTMLTextAreaElement>('.agent-composer textarea')!
const groupOf = (element: Element) => element.closest<HTMLElement>('.session-group')!
const fileTab = (name: string) => screen.getByRole('tab', { name: new RegExp(name) })
let navigate: ReturnType<typeof useNavigate>
function Navigation() { const nav = useNavigate(); useEffect(() => { navigate = nav }, [nav]); return null }
function mount(withList = false) {
  return render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider><RunnerCommands onNewSession={() => {}}/><Navigation/><div style={{ height: '100vh', display: 'flex' }}>{withList && <SessionList variant="sidebar" onNewSession={() => {}}/>}<div style={{ flex: 1, minWidth: 0 }}><Routes><Route path="/sessions/:id" element={<SessionDetail/>}/></Routes></div></div></CommandPaletteProvider></MemoryRouter>)
}
async function command(title: string) {
  await userEvent.keyboard('{Control>}k{/Control}')
  await userEvent.fill(screen.getByRole('combobox', { name: /^Search commands/ }), `>${title}`)
  await userEvent.keyboard('{Enter}')
}
beforeEach(() => {
  localStorage.clear(); localStorage.setItem(RAIL_PREF_KEY, '0')
  document.documentElement.setAttribute('data-theme', 'dark')
  localStorage.setItem(key, JSON.stringify({ tabs: paths.map(path => ({ id: `workspace:${path}`, view: { kind: 'workspace', path }, preview: false })), active: 'chat' }))
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: ['sess-a', 'sess-b'].map(id => makeSession({ id, kind: 'agent', status: 'idle', title: 'Build split views' })) })
  useTranscriptStore.setState({ sessions: {} })
  for (const id of ['sess-a', 'sess-b']) {
    const store = useTranscriptStore.getState()
    store.ingest({ session_id: id, seq: 1, client_event_id: `split-${id}`, ts: '2026-10-25T12:00:00Z', kind: 'assistant_text', payload: { text: 'Review the source and release notes beside this conversation. Each group keeps its own selected tab.', done: true } } as AgentEvent)
    store.ingestReplayDone(id, { lastSeq: 1, hasMore: false })
  }
  apiFetch.mockReset().mockImplementation(async (url: string) => url.includes('/live') ? json({ can_release_prompt: true }) : url.endsWith('/shells') ? json([]) : url.endsWith('/shell-capability') ? json({ available: false }) : url.includes('/workspace?') ? json({ result: { ok: true, entries: paths.map(path => ({ path, name: path, directory: false })) }, members: [] }) : json({}))
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (String(url).includes('/workspace-file')) {
      const path = new URL(String(url), location.origin).searchParams.get('path')!
      const content = path.endsWith('.md') ? '# Release notes\n\nDrag a tab to an edge to split. Drop in the center to move it into a group.\n\n- Chat keeps the composer.\n- Each divider can be resized.\n' : Array.from({ length: 100 }, (_, i) => `export const value${i} = "${'source-code-'.repeat(30)}"`).join('\n')
      return json({ path, view: path.endsWith('.md') ? 'markdown' : 'text', content_type: 'text/plain', content_b64: btoa(content) })
    }
    return json(String(url).endsWith('/artifacts') ? { artifacts: [] } : [])
  }))
})
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals(); document.documentElement.removeAttribute('data-theme') })

it('splits, nests, resizes, scopes shortcuts, moves Chat with its draft and prunes an emptied group', async () => {
  await page.viewport(1800, 1100); mount()
  const input = composer(); await userEvent.fill(input, 'Keep this draft while moving Chat')
  await userEvent.click(fileTab('main.ts')); await screen.findByTestId('workspace-code')
  await command('Split tab group right')
  expect(groupNodes()).toHaveLength(2)
  expect(input).toBeVisible(); expect(input).toHaveValue('Keep this draft while moving Chat')
  const main = await screen.findByRole('region', { name: 'main.ts' })
  expect(groupOf(input)).not.toBe(groupOf(main))
  await command('Focus next tab group')
  expect(composer()).toHaveFocus()
  await userEvent.keyboard('{Alt>}2{/Alt}')
  expect(fileTab('notes.md')).toHaveAttribute('aria-selected', 'true')
  expect(fileTab('main.ts')).toHaveAttribute('aria-selected', 'true')
  await command('Split tab group down')
  expect(groupNodes()).toHaveLength(3)
  const dividers = screen.getAllByRole('separator', { name: /Resize tab groups/ })
  const down = dividers.find(node => node.getAttribute('aria-orientation') === 'horizontal')!
  await userEvent.click(down); await userEvent.keyboard('{ArrowDown}')
  expect(down).toHaveAttribute('aria-valuenow', '55')
  const right = dividers.find(node => node.getAttribute('aria-orientation') === 'vertical')!
  const box = right.getBoundingClientRect(), frame = window.frameElement?.getBoundingClientRect(), scale = frame ? frame.width / innerWidth : 1
  const x = (frame?.left ?? 0) + (box.left + box.width / 2) * scale, y = (frame?.top ?? 0) + (box.top + box.height / 2) * scale
  await cdp().send('Input.dispatchMouseEvent', { type: 'mousePressed', x, y, button: 'left', clickCount: 1 })
  await cdp().send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: x + 120 * scale, y, button: 'left', buttons: 1 })
  await cdp().send('Input.dispatchMouseEvent', { type: 'mouseReleased', x: x + 120 * scale, y, button: 'left', clickCount: 1 })
  await waitFor(() => expect(Number(right.getAttribute('aria-valuenow'))).toBeGreaterThan(50))
  await userEvent.click(fileTab('Chat'))
  await command('Move tab to next group')
  expect(composer()).toBe(input); expect(input).toBeVisible(); expect(input).toHaveValue('Keep this draft while moving Chat')
  await userEvent.click(fileTab('main.ts'))
  await command('Close current tab')
  expect(groupNodes()).toHaveLength(2)
  await command('Focus next tab group')
  expect(document.activeElement?.closest('.session-group')).toHaveClass('focused')
})

// The drop overlay (#245) is VS Code's: one translucent rectangle over the half of the hovered group the
// tab would take, or over the whole group for a move, and nothing else.
const overlays = () => [...document.querySelectorAll<HTMLElement>('[data-testid="drop-overlay"]')]
function expectRegion(overlay: HTMLElement, group: HTMLElement, zone: 'left' | 'right' | 'top' | 'bottom' | 'center', strip = 0) {
  const o = overlay.getBoundingClientRect(), g = group.getBoundingClientRect()
  const top = g.top + strip, height = g.height - strip, near = (a: number, b: number) => expect(Math.abs(a - b)).toBeLessThan(1.5)
  near(o.left, zone === 'right' ? g.left + g.width / 2 : g.left)
  near(o.right, zone === 'left' ? g.left + g.width / 2 : g.right)
  near(o.top, zone === 'bottom' ? top + height / 2 : top)
  near(o.bottom, zone === 'top' ? top + height / 2 : g.bottom)
  // Nothing else is drawn: no zone outlines, no labels, no arrows.
  expect(overlay.childElementCount).toBe(0); expect(overlay.textContent).toBe('')
  expect(document.querySelectorAll('.session-drop-zones, [data-testid="split-preview"]')).toHaveLength(0)
  expect(getComputedStyle(overlay).backgroundColor).toMatch(/rgba?\(.*,\s*0?\.\d+\)|color\(/)
}

it.each(['left', 'right', 'top', 'bottom'] as const)('highlights the %s half of the group and performs a native edge drag over viewer content', async edge => {
  await page.viewport(1600, 1000); mount()
  await userEvent.click(fileTab('main.ts')); await screen.findByTestId('workspace-code')
  const source = fileTab('notes.md').closest<HTMLElement>('.session-tab')!, group = groupNodes()[0]
  const rect = group.getBoundingClientRect()
  const position = { x: edge === 'left' ? 12 : edge === 'right' ? rect.width - 12 : rect.width / 2, y: edge === 'top' ? 55 : edge === 'bottom' ? rect.height - 12 : rect.height / 2 }
  // The overlay is asserted in the real layout before the trusted Playwright drag.
  const transfer = new DataTransfer()
  fireEvent.dragStart(source, { dataTransfer: transfer })
  fireEvent.dragOver(group.querySelector('.workspace-code') ?? group, { dataTransfer: transfer, clientX: rect.left + position.x, clientY: rect.top + position.y })
  await waitFor(() => expect(overlays()).toHaveLength(1))
  expect(overlays()[0]).toHaveAttribute('data-zone', edge)
  await waitFor(() => expectRegion(overlays()[0], group, edge))
  if (shotDir) await page.screenshot({ path: `${shotDir}/drop-overlay-${edge}.png` })
  // The center of the group moves the tab into it: the whole group is highlighted.
  fireEvent.dragOver(group.querySelector('.workspace-code') ?? group, { dataTransfer: transfer, clientX: rect.left + rect.width / 2, clientY: rect.top + rect.height / 2 })
  await waitFor(() => expect(overlays()[0]).toHaveAttribute('data-zone', 'center'))
  await waitFor(() => expectRegion(overlays()[0], group, 'center'))
  fireEvent.dragEnd(source, { dataTransfer: transfer })
  expect(overlays()).toHaveLength(0)
  await userEvent.dragAndDrop(source, group, { targetPosition: position })
  await waitFor(() => expect(groupNodes()).toHaveLength(2))
  const notes = await screen.findByRole('heading', { name: 'Release notes' })
  expect(groupOf(notes)).not.toBe(groupOf(screen.getByRole('region', { name: 'main.ts' })))
  expect(persisted().layout.axis).toBe(edge === 'left' || edge === 'right' ? 'vertical' : 'horizontal')
  // A center drop joins groups and removes the emptied source group.
  const target = groupOf(screen.getByRole('region', { name: 'main.ts' })), box = target.getBoundingClientRect()
  await userEvent.dragAndDrop(fileTab('notes.md').closest<HTMLElement>('.session-tab')!, target, { targetPosition: { x: box.width / 2, y: box.height / 2 } })
  expect(groupNodes()).toHaveLength(1)
})

it.each(['left', 'right', 'top', 'bottom', 'center'] as const)('in a split the %s overlay covers that region of the group below its tab strip', async zone => {
  await page.viewport(2000, 1100); mount()
  await userEvent.click(fileTab('main.ts')); await command('Split tab group right')
  await command('Focus next tab group'); await userEvent.click(fileTab('Chat'))
  const group = groupOf(fileTab('main.ts')), strip = group.querySelector<HTMLElement>('.session-tabbar')!.getBoundingClientRect().height
  const content = group.querySelector<HTMLElement>('.session-group-content')!, rect = group.getBoundingClientRect()
  const x = zone === 'left' ? rect.left + 10 : zone === 'right' ? rect.right - 10 : rect.left + rect.width / 2
  const y = zone === 'top' ? rect.top + strip + 10 : zone === 'bottom' ? rect.bottom - 10 : rect.top + strip + (rect.height - strip) / 2
  const transfer = new DataTransfer(), source = fileTab('notes.md').closest<HTMLElement>('.session-tab')!
  fireEvent.dragStart(source, { dataTransfer: transfer })
  fireEvent.dragOver(content, { dataTransfer: transfer, clientX: x, clientY: y })
  await waitFor(() => expect(overlays()).toHaveLength(1))
  expect(groupOf(overlays()[0])).toBe(group)
  expect(overlays()[0]).toHaveAttribute('data-zone', zone)
  await waitFor(() => expectRegion(overlays()[0], group, zone, strip))
  if (shotDir && (zone === 'right' || zone === 'center')) await page.screenshot({ path: `${shotDir}/split-drop-overlay-${zone}.png` })
  fireEvent.dragEnd(source, { dataTransfer: transfer })
})

it('over a tab strip draws only an insertion marker between tabs, and drops the tab there', async () => {
  await page.viewport(2000, 1100); mount()
  await userEvent.click(fileTab('main.ts')); await command('Split tab group right')
  act(() => { openSessionFile({ session: 'sess-a', account: 'person-a', path: 'new.ts' }) })
  await screen.findByRole('region', { name: 'new.ts' })
  const left = groupOf(fileTab('notes.md')), strip = left.querySelector<HTMLElement>('[role=tablist]')!
  const notes = fileTab('notes.md').closest<HTMLElement>('.session-tab')!, tests = fileTab('tests.ts').closest<HTMLElement>('.session-tab')!
  // Native events carry one DataTransfer throughout, as a real drag does (fireEvent copies it empty).
  const transfer = new DataTransfer(), source = fileTab('new.ts').closest<HTMLElement>('.session-tab')!
  const fire = (target: Element, type: string, x = 0, y = 0) => act(() => { target.dispatchEvent(new DragEvent(type, { bubbles: true, cancelable: true, composed: true, dataTransfer: transfer, clientX: x, clientY: y })) })
  fire(source, 'dragstart')
  // The left half of a tab: the marker sits at that tab's left edge.
  const t = tests.getBoundingClientRect()
  fire(tests, 'dragover', t.left + 4, t.top + t.height / 2)
  const marker = await within(left).findByTestId('tab-insert-marker')
  await waitFor(() => expect(Math.abs(marker.getBoundingClientRect().left + 1 - t.left)).toBeLessThan(1.5))
  expect(overlays()).toHaveLength(0)
  expect(document.querySelectorAll('[data-testid="tab-insert-marker"]')).toHaveLength(1)
  if (shotDir) await page.screenshot({ path: `${shotDir}/split-insert-marker.png` })
  // The right half of the last tab: the marker sits after it.
  fire(tests, 'dragover', t.right - 4, t.top + t.height / 2)
  await waitFor(() => expect(Math.abs(within(left).getByTestId('tab-insert-marker').getBoundingClientRect().left + 1 - t.right)).toBeLessThan(1.5))
  // The right half of notes.md is the same gap as the left half of tests.ts.
  const n = notes.getBoundingClientRect()
  fire(notes, 'dragover', n.right - 4, n.top + n.height / 2)
  await waitFor(() => expect(Math.abs(within(left).getByTestId('tab-insert-marker').getBoundingClientRect().left + 1 - t.left)).toBeLessThan(1.5))
  fire(notes, 'drop', n.right - 4, n.top + n.height / 2)
  expect(screen.queryByTestId('tab-insert-marker')).toBeNull()
  expect(groupNodes()).toHaveLength(2)
  expect([...strip.querySelectorAll<HTMLElement>('[role=tab]')].map(tab => tab.dataset.tabId)).toEqual(['chat', 'workspace:notes.md', 'workspace:new.ts', 'workspace:tests.ts'])
})

it('opens files in the group clicked through a portal and ignores foreign and canceled drags', async () => {
  await page.viewport(1600, 1000); mount()
  await userEvent.click(fileTab('main.ts')); await command('Split tab group right')
  await userEvent.click(composer())
  act(() => { openSessionFile({ session: 'sess-a', account: 'person-a', path: 'new.ts' }) })
  await screen.findByRole('region', { name: 'new.ts' })
  expect(groupOf(fileTab('new.ts'))).not.toBe(groupOf(fileTab('main.ts')))
  await userEvent.click(fileTab('main.ts'))
  await userEvent.click(screen.getByRole('button', { name: 'Toggle explorer' }))
  act(() => { openSessionFile({ session: 'sess-a', account: 'person-a', path: 'from-explorer.ts' }) })
  await screen.findByRole('region', { name: 'from-explorer.ts' })
  expect(groupOf(fileTab('from-explorer.ts'))).toBe(groupOf(fileTab('main.ts')))
  const before = persisted().layout, transfer = new DataTransfer(), group = groupNodes()[0]
  transfer.setData('text/plain', 'workspace:notes.md')
  fireEvent.dragOver(group, { dataTransfer: transfer }); fireEvent.drop(group, { dataTransfer: transfer })
  expect(persisted().layout).toEqual(before)
  fireEvent.dragStart(fileTab('notes.md').closest<HTMLElement>('.session-tab')!, { dataTransfer: new DataTransfer() })
  await userEvent.keyboard('{Escape}')
  expect(screen.queryByTestId('drop-overlay')).toBeNull()
  expect(persisted().layout).toEqual(before)
})

it.each([1024, 2860, 3840])('keeps split tab strips on the top row with the toggles at its ends, beside the session list, explorer and panel at %i px', async width => {
  await page.viewport(width, 1100); localStorage.setItem(RAIL_PREF_KEY, '1'); mount(true)
  await userEvent.click(fileTab('main.ts'))
  // Single view: one strip across the explorer, carrying every toggle.
  const single = screen.getByRole('navigation', { name: 'Workspace' }).getBoundingClientRect()
  const singleTab = fileTab('main.ts').getBoundingClientRect()
  if (shotDir) await page.screenshot({ path: `${shotDir}/single-workbench-${width}.png` })
  await command('Split tab group right')
  await userEvent.click(screen.getByRole('button', { name: 'Toggle explorer' }))
  await screen.findByRole('region', { name: 'Explorer' })
  const columns = screen.getByTestId('session-columns')
  await waitFor(() => {
    expect(columns.scrollWidth).toBe(columns.clientWidth); expect(columns.scrollHeight).toBe(columns.clientHeight)
    expect(screen.getByTestId('session-chat-column').clientWidth).toBeGreaterThanOrEqual(320)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })
  expect(groupNodes()).toHaveLength(2)
  expect(composer()).toBeVisible()
  // No workspace bar of its own: every strip in the top row sits where the single view's strip sat.
  expect(screen.queryByRole('navigation', { name: 'Workspace' })).toBeNull()
  const strips = groupNodes().map(group => group.querySelector<HTMLElement>('.session-tabbar')!)
  const stacked = width === 1024
  const top = stacked ? [strips[0]] : strips
  for (const strip of top) {
    const r = strip.getBoundingClientRect()
    expect(Math.abs(r.top - single.top)).toBeLessThan(1); expect(Math.abs(r.height - single.height)).toBeLessThan(1)
  }
  expect(Math.abs(within(strips[0]).getByRole('tab', { name: /Chat/ }).getBoundingClientRect().top - singleTab.top)).toBeLessThan(1)
  // The explorer toggle at the left end of the first strip; shell, board and panel at the right end of
  // the top-right strip (the top group's strip when the groups stack).
  const first = strips[0], last = stacked ? strips[0] : strips[1]
  const explorerToggle = screen.getByRole('button', { name: 'Toggle explorer' })
  expect(screen.getAllByRole('button', { name: 'Toggle explorer' })).toHaveLength(1)
  expect(first.firstElementChild).toBe(explorerToggle)
  const panel = screen.getByTestId('rail-toggle')
  expect(screen.getAllByTestId('rail-toggle')).toHaveLength(1)
  expect(last.lastElementChild).toBe(panel)
  expect(last.getBoundingClientRect().right - panel.getBoundingClientRect().right).toBeLessThan(12)
  if (!stacked) {
    expect(within(strips[1]).queryByRole('button', { name: 'Toggle explorer' })).toBeNull()
    expect(within(strips[0]).queryByTestId('rail-toggle')).toBeNull()
  }
  expect(within(stacked ? strips[1] : strips[0]).queryByTestId('rail-toggle')).toBeNull()
  if (width === 1024) {
    expect(screen.queryByTestId('session-rail')).toBeNull()
    expect(screen.getByRole('separator', { name: /Resize tab groups/ })).toHaveAttribute('aria-orientation', 'horizontal')
    expect(within(strips[1]).queryByRole('button', { name: 'Toggle explorer' })).toBeNull()
    await userEvent.click(panel)
    expect(screen.getByRole('dialog', { name: 'Session panel' })).toBeVisible()
    expect(columns.scrollWidth).toBe(columns.clientWidth)
    await userEvent.keyboard('{Escape}')
  } else expect(screen.getByTestId('session-rail')).toBeVisible()
  if (shotDir) await page.screenshot({ path: `${shotDir}/split-workbench-${width}.png` })
  // The explorer toggle still works by keyboard from its new place.
  explorerToggle.focus(); await userEvent.keyboard('{Enter}')
  expect(screen.queryByRole('region', { name: 'Explorer' })).toBeNull()
  expect(explorerToggle).toHaveAttribute('aria-expanded', 'false')
})

it('Focus mode retains the focused group with several visible files or Chat', async () => {
  await page.viewport(1800, 1100); mount()
  await userEvent.click(fileTab('main.ts')); await command('Split tab group right')
  await command('Focus next tab group'); await userEvent.click(fileTab('notes.md'))
  await command('Focus next tab group')
  await command('Focus mode')
  expect(screen.getByRole('region', { name: 'main.ts' })).toHaveFocus()
  await command('Focus mode')
  await userEvent.click(fileTab('Chat'))
  await command('Focus mode')
  expect(composer()).toHaveFocus()
})

it('reorders and numbers file-only groups independently of the pinned Chat group', async () => {
  await page.viewport(1800, 1100); mount()
  await userEvent.click(fileTab('main.ts')); await command('Split tab group right')
  act(() => { openSessionFile({ session: 'sess-a', account: 'person-a', path: 'new.ts' }) })
  await screen.findByRole('region', { name: 'new.ts' })
  await userEvent.click(fileTab('new.ts'))
  await userEvent.keyboard('{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}')
  const group = groupOf(fileTab('new.ts'))
  expect([...group.querySelectorAll<HTMLElement>('[role="tab"]')].map(tab => tab.dataset.tabId)).toEqual(['workspace:new.ts', 'workspace:main.ts'])
  await userEvent.keyboard('{Alt>}2{/Alt}')
  expect(screen.getByRole('region', { name: 'main.ts' })).toHaveFocus()
  expect(composer()).toBeVisible()
})


it('cleans up targets when navigating between terminal sessions with the same tab ids', async () => {
  await page.viewport(1800, 1100)
  useSessionStore.setState(state => ({ sessions: state.sessions.map(s => ({ ...s, kind: 'terminal' })) }))
  localStorage.setItem(sessionTabsKey('person-a', 'sess-b'), localStorage.getItem(key)!)
  mount()
  await userEvent.click(fileTab('main.ts')); await screen.findByRole('region', { name: 'main.ts' })
  for (const id of ['sess-b', 'sess-a', 'sess-b']) {
    act(() => navigate(`/sessions/${id}`))
    await userEvent.click(fileTab('main.ts')); await screen.findByRole('region', { name: 'main.ts' })
    const targets = document.querySelectorAll('.session-view-target')
    expect([...targets].filter(t => t.id === 'session-view-chat')).toHaveLength(1)
    expect([...targets].filter(t => t.id === 'session-view-workspace%3Amain.ts')).toHaveLength(1)
    expect(targets).toHaveLength(paths.length + 1)
  }
})

it('gives each group a distinct name, announces focus and reports the drawn divider ratio', async () => {
  await page.viewport(1024, 900); mount()
  await userEvent.click(fileTab('main.ts')); await command('Split tab group right')
  expect(screen.getByRole('tablist', { name: 'Session tabs, group 1' })).toBeVisible()
  expect(screen.getByRole('tablist', { name: 'Session tabs, group 2' })).toBeVisible()
  expect(screen.getByRole('navigation', { name: 'Workspace group 1' })).toBeVisible()
  expect(screen.getByRole('navigation', { name: 'Workspace group 2' })).toHaveAttribute('aria-current', 'true')
  expect(screen.getByTestId('group-announce')).toHaveTextContent(/^Focused tab group 2 of 2, 1 tab$/)
  await command('Focus next tab group')
  expect(screen.getByTestId('group-announce')).toHaveTextContent(/^Focused tab group 1 of 2, 3 tabs$/)
  // Closing a tab in the focused group does not announce again.
  await userEvent.click(within(groupNodes()[0]).getByRole('button', { name: 'Close notes.md' }))
  expect(within(groupNodes()[0]).queryByRole('tab', { name: /notes\.md/ })).toBeNull()
  expect(screen.getByTestId('group-announce')).toHaveTextContent(/^Focused tab group 1 of 2, 3 tabs$/)
  const divider = screen.getByRole('separator', { name: /Resize tab groups/ })
  await userEvent.click(divider); await userEvent.keyboard('{Home}')
  const controls = divider.getAttribute('aria-controls')!.split(' ')
  expect(controls).toHaveLength(2)
  expect(controls.every(id => document.getElementById(id))).toBe(true)
  expect(Number(divider.getAttribute('aria-valuenow'))).toBeGreaterThan(15)
})

it('drops a tab from another group before the tab whose left half is under the pointer', async () => {
  await page.viewport(1800, 1100); mount()
  await userEvent.click(fileTab('main.ts')); await command('Split tab group right')
  await userEvent.dragAndDrop(fileTab('main.ts').closest<HTMLElement>('.session-tab')!, fileTab('tests.ts'), { targetPosition: { x: 4, y: 10 } })
  expect(groupNodes()).toHaveLength(1)
  expect([...screen.getByRole('tablist', { name: 'Session tabs' }).querySelectorAll<HTMLElement>('[role=tab]')].map(t => t.dataset.tabId)).toEqual(['chat', 'workspace:notes.md', 'workspace:main.ts', 'workspace:tests.ts'])
})

it.each([{ prune: false, fallback: false }, { prune: false, fallback: true }, { prune: true, fallback: false }])('retains viewer scroll when moving to another group (source pruned=$prune, fallback=$fallback)', async ({ prune, fallback }) => {
  const descriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'moveBefore')
  if (fallback) Object.defineProperty(HTMLElement.prototype, 'moveBefore', { configurable: true, value: undefined })
  try {
    await page.viewport(1800, 1100); mount()
    await userEvent.click(fileTab('main.ts')); await screen.findByTestId('workspace-code')
    await command('Split tab group right')
    if (!prune) {
      act(() => { openSessionFile({ session: 'sess-a', account: 'person-a', path: 'new.ts' }) })
      await screen.findByRole('region', { name: 'new.ts' }); await userEvent.click(fileTab('main.ts'))
    }
    const code = within(screen.getByRole('region', { name: 'main.ts' })).getByTestId('workspace-code')
    code.scrollTop = 190; code.scrollLeft = 80
    expect(code.scrollTop).toBe(190)
    await command('Move tab to next group')
    expect(within(screen.getByRole('region', { name: 'main.ts' })).getByTestId('workspace-code')).toBe(code)
    expect(code.scrollTop).toBe(190); expect(code.scrollLeft).toBe(80)
  } finally {
    if (fallback) { if (descriptor) Object.defineProperty(HTMLElement.prototype, 'moveBefore', descriptor); else Reflect.deleteProperty(HTMLElement.prototype, 'moveBefore') }
  }
})


it('keeps an iframe document when an atomic move can retain both groups', async () => {
  if (!('moveBefore' in Element.prototype)) return
  await page.viewport(1800, 1100); mount()
  await userEvent.click(fileTab('main.ts')); await command('Split tab group right')
  act(() => { openSessionFile({ session: 'sess-a', account: 'person-a', path: 'new.ts' }) })
  await screen.findByRole('region', { name: 'new.ts' }); await userEvent.click(fileTab('main.ts'))
  const target = groupOf(fileTab('main.ts')).querySelector<HTMLElement>('[data-view-id="workspace:main.ts"]')!
  const frame = document.createElement('iframe')
  frame.srcdoc = '<input value="Initial document">'
  const loaded = new Promise<void>(resolve => { frame.onload = () => resolve() })
  target.append(frame); await loaded
  const documentBefore = frame.contentDocument!
  documentBefore.querySelector('input')!.value = 'State after loading'
  await command('Move tab to next group')
  expect(frame.contentDocument).toBe(documentBefore)
  expect(frame.contentDocument!.querySelector('input')!.value).toBe('State after loading')
})

it.each([390, 1024, 3840])('focuses the active tab and restores the workbench at %i px', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160); const view = mount(width !== 390)
  if (width !== 390) {
    await userEvent.click(fileTab('main.ts')); await command('Split tab group right')
    const divider = screen.getByRole('separator', { name: /Resize tab groups/ })
    divider.focus(); await userEvent.keyboard('{ArrowLeft}')
  }
  const before = persisted(), nodes = groupNodes()
  const explorerRegion = () => screen.getByRole(width === 390 ? 'dialog' : 'region', { name: 'Explorer' })
  if (width !== 390) await command('Toggle explorer')
  if (width === 3840) {
    if (screen.getByTestId('rail-toggle').getAttribute('aria-expanded') === 'false') await userEvent.click(screen.getByTestId('rail-toggle'))
    await screen.findByRole('separator', { name: 'Session panel width' })
    for (const name of ['Explorer width', 'Session list width', 'Session panel width']) {
      const divider = screen.getByRole('separator', { name })
      divider.focus(); await userEvent.keyboard('{ArrowRight}')
    }
  }
  const paneWidths = [...document.querySelectorAll<HTMLElement>('.side-pane:not([hidden])')].map(pane => [pane.getAttribute('aria-label'), pane.getBoundingClientRect().width])
  const explorer = width === 390 ? 0 : explorerRegion().getBoundingClientRect().width
  await command('Focus mode')
  expect(screen.queryByRole('region', { name: 'Explorer' })).toBeNull()
  expect(screen.queryByRole('region', { name: 'Session list' })).toBeNull()
  expect(groupNodes()).toEqual(nodes)
  expect(nodes.filter(n => n.checkVisibility())).toHaveLength(1)
  expect(screen.queryByRole('separator', { name: /Resize tab groups/ })).toBeNull()
  expect(screen.getByTestId('tab-focus-announce')).toHaveTextContent('Entered tab focus mode')
  await waitFor(() => {
    const visible = nodes.find(n => n.checkVisibility())!.getBoundingClientRect()
    const area = screen.getByTestId('session-groups').getBoundingClientRect()
    expect(visible.width).toBe(area.width); expect(visible.height).toBe(area.height)
  })
  const exit = screen.getByRole('button', { name: 'Exit tab focus' })
  expect(exit.title).toMatch(/Shift.*Enter/)
  const shots = import.meta.env.VITE_TAB_FOCUS_SHOTS_DIR
  if (shots) await page.screenshot({ path: `${shots}/tab-focus-${width}.png` })
  await userEvent.click(exit)
  if (width !== 390) await waitFor(() => expect(explorerRegion().getBoundingClientRect().width).toBe(explorer))
  await waitFor(() => expect([...document.querySelectorAll<HTMLElement>('.side-pane:not([hidden])')].map(pane => [pane.getAttribute('aria-label'), pane.getBoundingClientRect().width])).toEqual(paneWidths))
  expect(persisted().layout).toEqual(before.layout)
  expect(persisted().focused).toBe(before.focused)
  expect(nodes.every(n => n.checkVisibility())).toBe(true)
  expect(screen.getByTestId('tab-focus-announce')).toHaveTextContent('Exited tab focus mode')
  if (shots) await page.screenshot({ path: `${shots}/tab-focus-restored-${width}.png` })
  if (width === 390) return
  const region = await screen.findByRole('region', { name: 'main.ts' })
  await userEvent.dblClick(fileTab('main.ts'))
  expect(region).toHaveFocus()
  await userEvent.dblClick(fileTab('main.ts'))
  expect(screen.queryByRole('button', { name: 'Exit tab focus' })).toBeNull()
  await userEvent.dblClick(fileTab('main.ts'))
  await userEvent.keyboard('{Control>}{Shift>}{Enter}{/Shift}{/Control}')
  expect(screen.queryByRole('button', { name: 'Exit tab focus' })).toBeNull()
  await userEvent.keyboard('{Control>}{Shift>}{Enter}{/Shift}{/Control}')
  act(() => openSessionFile({ session: 'sess-a', account: 'person-a', path: 'new.ts' }))
  expect(await screen.findByRole('region', { name: 'new.ts' })).toBeVisible()
  expect(nodes.filter(n => n.checkVisibility())).toHaveLength(1)
  await userEvent.keyboard('{Control>}p{/Control}')
  await userEvent.fill(screen.getByRole('combobox', { name: 'Search session files' }), 'notes.md')
  await screen.findByRole('option', { name: /notes.md/ })
  await userEvent.keyboard('{Enter}')
  expect(await screen.findByRole('heading', { name: 'Release notes' })).toBeVisible()
  expect(nodes.filter(n => n.checkVisibility())).toHaveLength(1)
  await command('Back to Chat')
  expect(composer()).toHaveFocus()
  expect(nodes.filter(n => n.checkVisibility())).toHaveLength(1)
  await userEvent.click(fileTab('notes.md'))
  expect(await screen.findByRole('heading', { name: 'Release notes' })).toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: 'Close tests.ts' }))
  expect(screen.getByRole('button', { name: 'Exit tab focus' })).toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: 'Close notes.md' }))
  expect(screen.queryByRole('button', { name: 'Exit tab focus' })).toBeNull()
  await command('Focus mode')
  act(() => navigate('/sessions/sess-b'))
  expect(screen.queryByRole('button', { name: 'Exit tab focus' })).toBeNull()
  act(() => navigate('/sessions/sess-a'))
  expect(screen.queryByRole('button', { name: 'Exit tab focus' })).toBeNull()
  await command('Focus mode')
  view.unmount(); mount(true)
  expect(screen.queryByRole('button', { name: 'Exit tab focus' })).toBeNull()
  expect(groupNodes()).toHaveLength(2)
})


it('exits focus in the group of the last tab selected through quick open', async () => {
  await page.viewport(3840, 1200); mount()
  await userEvent.click(fileTab('main.ts')); await command('Split tab group right')
  const original = groupOf(fileTab('main.ts'))
  await userEvent.dblClick(fileTab('main.ts'))
  await userEvent.keyboard('{Control>}p{/Control}')
  await userEvent.fill(screen.getByRole('combobox', { name: 'Search session files' }), 'notes.md')
  await screen.findByRole('option', { name: /notes.md/ })
  await userEvent.keyboard('{Enter}')
  const last = groupOf(fileTab('notes.md'))
  expect(last).not.toBe(original)
  await userEvent.click(screen.getByRole('button', { name: 'Exit tab focus' }))
  expect(last).toHaveClass('focused')
  expect(screen.getByRole('region', { name: 'notes.md' })).toHaveFocus()
  expect(groupNodes().every(node => node.checkVisibility())).toBe(true)
})
