import { useLayoutEffect } from 'react'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { beforeEach, expect, it } from 'vitest'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import CommandPaletteProvider from './CommandPalette'
import SessionTabBar from './SessionTabBar'
import SessionGroups, { sessionTabDragType } from './SessionGroups'
import { sessionTabsKey, useSessionTabs, type SessionTabs } from '../hooks/useSessionTabs'
import { useExplorerLayout } from '../hooks/useExplorerLayout'
import { useIsMobile } from '../hooks/useIsMobile'
import '../index.css'

let tabs: SessionTabs
const names = Array.from({ length: 20 }, (_, i) => `feature-${String(i).padStart(2, '0')}-with-a-long-file-name-for-overflow.ts`)
function Workspace() {
  const state = useSessionTabs('owner', 'overflow', useIsMobile())
  useLayoutEffect(() => { tabs = state })
  const explorer = useExplorerLayout(false)
  const bar = (group: typeof state.groups[number], ends = { start: true, end: true }) => <SessionTabBar tabs={state} explorer={explorer} group={group} grouped={!state.phone && state.groups.length > 1} {...ends} shellOpen={false} shell={() => state.open({ kind: 'shell', shellId: 'new', label: 'Newly opened shell', runtime: 'host', folder: 'project' })} panel={<button className="icon-btn" aria-label="Panel">▥</button>} />
  return <div style={{ height: '100dvh', display: 'flex', flexDirection: 'column', minWidth: 0 }}>
    {(state.phone || state.groups.length === 1) && bar(state.groups[0])}
    <SessionGroups tabs={state} bar={bar} />
  </div>
}
function mount() { return render(<MemoryRouter initialEntries={['/sessions/overflow']}><CommandPaletteProvider><Workspace /></CommandPaletteProvider></MemoryRouter>) }
beforeEach(() => {
  localStorage.clear()
  localStorage.setItem(sessionTabsKey('owner', 'overflow'), JSON.stringify({ tabs: names.map((path, i) => ({ id: `workspace:${path}`, view: { kind: 'workspace', path }, preview: i === names.length - 1 })), active: `workspace:${names[0]}` }))
})
const strip = () => document.querySelector<HTMLElement>('.session-tabs')!
function activeVisible(element = strip()) {
  const active = element.querySelector<HTMLElement>('.session-tab.active')!.getBoundingClientRect(), box = element.getBoundingClientRect()
  expect(active.left).toBeGreaterThanOrEqual(box.left - 1)
  expect(active.right).toBeLessThanOrEqual(box.right + 1)
}
for (const width of [390, 1024, 3840]) it(`scrolls overflowing tabs, reveals selections and pins tools at ${width}`, async () => {
  await page.viewport(width, width === 390 ? 844 : 1000); mount()
  const all = await screen.findByRole('button', { name: 'All tabs' })
  expect(strip().scrollWidth).toBeGreaterThan(strip().clientWidth)
  activeVisible()
  fireEvent.wheel(strip(), { deltaY: 180 })
  expect(strip().scrollLeft).toBeGreaterThan(0)
  const wheel = strip().scrollLeft
  fireEvent.wheel(strip(), { deltaY: 80, shiftKey: true })
  expect(strip().scrollLeft).toBeGreaterThan(wheel)
  fireEvent.wheel(strip(), { deltaX: 80 })
  expect(strip().scrollLeft).toBeGreaterThan(wheel + 79)
  await userEvent.click(screen.getByRole('button', { name: 'New shell' }))
  await waitFor(() => activeVisible())
  expect(tabs.active).toBe('shell:new')
  const tools = [...document.querySelectorAll<HTMLElement>('nav .icon-btn')].filter(node => !node.closest('.session-tabstrip'))
  const bounds = strip().getBoundingClientRect()
  for (const tool of tools) {
    const b = tool.getBoundingClientRect()
    expect(b.bottom <= bounds.top || b.top >= bounds.bottom || b.right <= bounds.left + 1 || b.left >= bounds.right - 1).toBe(true)
  }
  await userEvent.click(all)
  const menu = screen.getByRole('menu', { name: 'All tabs' })
  expect(within(menu).getAllByRole('menuitemradio')).toHaveLength(22)
  expect(within(menu).getByRole('menuitemradio', { checked: true })).toHaveTextContent('Newly opened shell')
  expect(within(menu).getAllByRole('menuitemradio', { checked: false })).toHaveLength(21)
  expect(within(menu).getByRole('menuitemradio', { name: new RegExp(`${names.at(-1)}.*Preview`) })).toBeVisible()
  expect(menu.querySelectorAll('.session-tab-menu-icon')).toHaveLength(22)
  const shots = import.meta.env.VITE_TAB_FOCUS_SHOTS_DIR
  if (shots) await page.screenshot({ path: `${shots}/tab-overflow-menu-${width}.png` })
  await userEvent.click(within(menu).getByRole('menuitemradio', { name: names[0] }))
  await waitFor(() => activeVisible())
  expect(tabs.active).toBe(`workspace:${names[0]}`)
  const selected = strip().querySelector<HTMLButtonElement>('[role=tab][aria-selected=true]')!
  selected.focus(); await userEvent.keyboard('{End}')
  await waitFor(() => activeVisible())
  expect(tabs.active).toBe('shell:new')
  expect(strip().querySelector('[role=tab][aria-selected=true]')).toHaveFocus()
  if (shots) await page.screenshot({ path: `${shots}/tab-overflow-${width}.png` })
}, 30000)

it('keeps each split overflow menu scoped and reveals the active tab in focus mode', async () => {
  await page.viewport(3840, 1200); mount()
  const extra = names.map(name => `extra-${name}`)
  act(() => { for (const path of extra) tabs.open({ kind: 'workspace', path }, true) })
  act(() => tabs.drop(`workspace:${names[0]}`, 'main', 'right'))
  const right = tabs.focused
  act(() => { for (const name of names.slice(1)) tabs.drop(`workspace:${name}`, right) })
  const groups = [...document.querySelectorAll<HTMLElement>('.session-group')]
  await waitFor(() => expect(screen.getAllByRole('button', { name: 'All tabs' })).toHaveLength(2))
  for (const group of groups) {
    await userEvent.click(within(group).getByRole('button', { name: 'All tabs' }))
    const menu = screen.getByRole('menu', { name: 'All tabs' })
    expect(within(menu).getAllByRole('menuitemradio')).toHaveLength(tabs.groups.find(g => g.id === group.dataset.groupId)!.tabs.length)
    await userEvent.keyboard('{Escape}')
  }
  act(() => tabs.toggleTabFocus?.())
  expect(groups.filter(group => group.checkVisibility())).toHaveLength(1)
  await userEvent.click(screen.getByRole('button', { name: 'All tabs' }))
  await userEvent.click(within(screen.getByRole('menu', { name: 'All tabs' })).getByRole('menuitemradio', { name: names[0] }))
  await waitFor(() => activeVisible(groups.find(group => group.checkVisibility())!.querySelector('.session-tabs')!))
})

it('auto-scrolls in both directions, reveals after reordering and preserves scroll after canceled drag', async () => {
  await page.viewport(1024, 900); mount()
  await screen.findByRole('button', { name: 'All tabs' })
  const element = strip(), source = element.querySelector<HTMLElement>('.session-tab.active')!, box = element.getBoundingClientRect(), transfer = new DataTransfer()
  const drag = (target: Element, type: string, x = 0) => act(() => { target.dispatchEvent(new DragEvent(type, { bubbles: true, cancelable: true, composed: true, dataTransfer: transfer, clientX: x, clientY: box.top + 12 })) })
  drag(source, 'dragstart')
  expect(transfer.types).toContain(sessionTabDragType)
  drag(element, 'dragover', box.right - 2)
  await waitFor(() => expect(element.scrollLeft).toBeGreaterThan(50))
  drag(element, 'drop', box.right - 2)
  await waitFor(() => activeVisible(element))
  expect(tabs.tabs[0].id).not.toBe(`workspace:${names[0]}`)
  act(() => tabs.select(`workspace:${names.at(-1)}`))
  await waitFor(() => activeVisible(element))
  const end = element.scrollLeft
  drag(element.querySelector('.session-tab.active')!, 'dragstart')
  drag(element, 'dragover', box.left + 2)
  await waitFor(() => expect(element.scrollLeft).toBeLessThan(end - 50))
  const canceled = element.scrollLeft
  drag(source, 'dragend')
  await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))
  expect(element.scrollLeft).toBe(canceled)
})

it('shows no overflow control when every tab fits', async () => {
  localStorage.clear(); await page.viewport(1024, 900); mount()
  expect(screen.queryByRole('button', { name: 'All tabs' })).toBeNull()
})

it('preserves manual scrolling on repeated selection of the current tab', async () => {
  await page.viewport(1024, 900); mount()
  await screen.findByRole('button', { name: 'All tabs' })
  const path = names.at(-1)!
  act(() => tabs.select(`workspace:${path}`))
  await waitFor(() => activeVisible())
  strip().scrollLeft = 0
  await userEvent.click(screen.getByRole('button', { name: 'All tabs' }))
  await userEvent.click(within(screen.getByRole('menu', { name: 'All tabs' })).getByRole('menuitemradio', { name: new RegExp(path) }))
  expect(strip().scrollLeft).toBe(0)
  act(() => tabs.open({ kind: 'workspace', path }))
  expect(strip().scrollLeft).toBe(0)
  const button = strip().querySelector<HTMLButtonElement>('[aria-selected=true]')!
  button.focus(); strip().scrollLeft = 0
  await userEvent.keyboard('{End}')
  expect(strip().scrollLeft).toBe(0)
})

for (const width of [390, 3840]) it(`keeps previews without toggling focus and keeps phone navigation unambiguous at ${width}`, async () => {
  await page.viewport(width, 1000); mount()
  const preview = () => screen.getByRole('tab', { name: new RegExp(names.at(-1)!) })
  act(() => tabs.select(`workspace:${names.at(-1)}`))
  await userEvent.dblClick(preview())
  expect(tabs.tabs.at(-1)!.preview).toBe(false)
  expect(tabs.tabFocused).toBe(false)
  // The next double-click is on a kept tab and maximizes it.
  await userEvent.dblClick(preview())
  expect(tabs.tabFocused).toBe(true)
  act(() => tabs.open({ kind: 'workspace', path: 'another-preview.ts' }))
  await userEvent.dblClick(screen.getByRole('tab', { name: /another-preview/ }))
  expect(tabs.tabs.at(-1)!.preview).toBe(false)
  expect(tabs.tabFocused).toBe(true)
  await userEvent.dblClick(screen.getByRole('tab', { name: /another-preview/ }))
  expect(tabs.tabFocused).toBe(false)
  if (width === 390) {
    await userEvent.dblClick(screen.getByRole('button', { name: /← Chat/ }))
    expect(tabs.active).toBe('chat')
    expect(tabs.tabFocused).toBe(false)
    await userEvent.click(screen.getByRole('button', { name: /open files/ }))
    const menu = screen.getByRole('menu', { name: 'Kept open' })
    expect(within(menu).queryByRole('menuitem', { name: 'Chat' })).toBeNull()
    expect(within(menu).getAllByRole('menuitem')).toHaveLength(tabs.tabs.length)
  }
})

// Wait for actual layout delivery rather than a wall-clock delay.
async function resized(element: HTMLElement, change: () => void) {
  const width = element.getBoundingClientRect().width
  await new Promise<void>(resolve => {
    const observer = new ResizeObserver(() => {
      if (element.getBoundingClientRect().width === width) return
      observer.disconnect(); resolve()
    })
    observer.observe(element); change()
  })
  await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))
}
it('preserves manual scrolling through badge width changes', async () => {
  await page.viewport(1024, 900); mount()
  await screen.findByRole('button', { name: 'All tabs' })
  act(() => tabs.host.agentOutput!('initial'))
  const element = strip(), chat = element.querySelector<HTMLElement>('.session-tab')!
  // Finish the initial overflow-control resize before simulating manual scrolling.
  await document.fonts.ready
  await new Promise<void>(resolve => {
    const observer = new ResizeObserver(() => { observer.disconnect(); resolve() })
    observer.observe(element)
  })
  element.scrollLeft = 500
  const scrolled = element.scrollLeft
  await resized(chat, () => act(() => tabs.host.agentOutput!('new output')))
  expect(element.scrollLeft).toBe(scrolled)
})
it('preserves manual scrolling through label width changes, but reveals on strip resize', async () => {
  await page.viewport(1024, 900); mount()
  await screen.findByRole('button', { name: 'All tabs' })
  const element = strip()
  const shell = { kind: 'shell' as const, shellId: 'label', label: 'Short', runtime: 'host' as const, folder: 'project' }
  act(() => tabs.open(shell))
  await waitFor(() => activeVisible())
  element.scrollLeft = 0
  const shellTab = element.querySelector<HTMLElement>('.session-tab.active')!
  await resized(shellTab, () => act(() => tabs.open({ ...shell, label: 'A considerably longer shell label' })))
  expect(element.scrollLeft).toBe(0)
  await resized(element, () => { element.parentElement!.style.maxWidth = '500px' })
  activeVisible()
  element.scrollLeft = 0
  act(() => tabs.select(`workspace:${names.at(-1)}`))
  await waitFor(() => activeVisible())
})


it('does not toggle focus from repeated phone back taps', async () => {
  await page.viewport(390, 844); mount()
  await userEvent.dblClick(screen.getByRole('button', { name: /← Chat/ }))
  expect(tabs.tabFocused).toBe(false)
})
it('lists only kept tabs in the phone kept-files menu', async () => {
  await page.viewport(390, 844); mount()
  await userEvent.click(screen.getByRole('button', { name: /open files/ }))
  const menu = screen.getByRole('menu', { name: 'Kept open' })
  expect(within(menu).queryByRole('menuitem', { name: 'Chat' })).toBeNull()
  expect(within(menu).getAllByRole('menuitem')).toHaveLength(19)
})
