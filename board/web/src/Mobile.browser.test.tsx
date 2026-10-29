// Exercise the Board app itself, with its real shared chat and deterministic API/transport fixtures.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import App from './App'
import './styles.css'
import { createFakeTransport } from '../../../packages/chat/src/test/fakeTransport'
import { resetChatState } from '../../../packages/chat/src/test/chat'

const chat = createFakeTransport({ files: false })
const session = { id: 'mobile-session', runner: 'fixture', external_session_id: 'fixture-session', lifecycle: 'idle', role: 'worker', resumable: true, created_at: '2026-01-01T00:00:00Z', model: 'claude-sonnet-5' }
const board = { id: 'b1', name: 'A board with a long descriptive name for the mobile layout', repos: ['example-org/example-repo'], field_schema: [], model: '', concurrency: 1 }
const columns = ['Inbox', 'Ready', 'Doing', 'Review', 'Done'].map((name, i) => ({ id: `col-${i}`, board_id: 'b1', name, rank: String(i), is_terminal: i === 4 }))
const card = { id: 'c1', board_id: 'b1', number: 1, rank: 'a', column_id: 'col-0', title: 'A card with a long title that should wrap comfortably on a phone', type: 'bug', priority: 'normal', body: '## Context\n\n' + 'LongUnbrokenReference'.repeat(12), fields: {}, repos: ['example-org/example-repo'], tags: [] as string[], links: [], depends_on: [], blockers: [], model: '', version: 1, auto_merge: false, run_attempts: 0, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' }
let boardSessions: typeof session[] = []
let boardCards: typeof card[] = [card]
let boardOverride: Record<string, unknown> = {}
const bareTargets = { can_choose: true, default_runtime: 'cluster', unresolved_repos: ['a-folder-with-a-fairly-long-name'], targets: [
  { runtime: 'cluster', label: 'Cluster', unavailable: 'a-folder-with-a-fairly-long-name is a desktop folder name' },
  { runtime: 'docker', daemon_id: 'desktop-a', desktop_name: 'A workstation with a long machine name', label: 'A workstation with a long machine name · Local sandbox' },
] }
const locationBefore = location.href
vi.mock('./api', async original => ({
  ...await original<typeof import('./api')>(),
  boardSocket: () => () => {},
  api: vi.fn(async (path: string) => {
    if (path === '/api/me') return { is_admin: true, is_human: true, kind: 'human', account_id: 'owner-fixture' }
    if (path === '/api/boards/b1/run-targets') return bareTargets
    if (path === '/api/boards/b1/launch-options') return { state: 'known', engines: [] }
    if (path.startsWith('/api/boards/b1/repo-remote?')) return { repo: 'a-folder-with-a-fairly-long-name', daemon_id: 'desktop-a', desktop_name: 'A workstation', has_folder: true, full_name: 'example-org/a-folder-with-a-fairly-long-name' }
    if (path === '/api/version') return { release: '0.4.4', commit: 'fixture' }
    if (path === '/api/boards/b1') return { ...board, ...boardOverride }
    if (path === '/api/boards/b1/columns') return columns
    if (path === '/api/boards/b1/cards') return boardCards
    if (path === '/api/cards/c1') return card
    if (path === '/api/cards/c1/runner-sessions') return [session]
    if (path === '/api/boards/b1/sessions') return boardSessions
    if (path === '/api/boards/b1/run') return { state: 'stopped', ready: 0, blocked: 0, in_flight: 0 }
    if (path === '/api/cards/c1/run-targets') return { can_choose: false, targets: [] }
    return []
  }),
}))
vi.mock('@blerglab/chat', async original => ({ ...await original<typeof import('@blerglab/chat')>(), createProxyTransport: () => chat.transport }))

beforeEach(() => { resetChatState(); boardSessions = []; boardCards = [card]; boardOverride = {}; chat.sent.length = 0; localStorage.clear(); sessionStorage.clear() })
afterEach(() => { cleanup(); history.replaceState({}, '', locationBefore); scrollTo(0, 0) })
// A busy board: 40 cards in the Inbox (a few tagged for the release), some in progress, more done.
function busyBoard() {
  const at = (n: number, col: number, over: Partial<typeof card> = {}) => ({ ...card, id: `k${n}`, number: n, rank: String(n).padStart(3, '0'), column_id: `col-${col}`, title: `Inbox card ${n} with a title long enough to wrap on a phone`, body: '', ...over })
  boardCards = [
    ...Array.from({ length: 40 }, (_, i) => at(i + 1, 0, i % 10 === 0 ? { tags: ['0.4.5'], type: 'feature' } : {})),
    ...[41, 42, 43].map(n => at(n, 2, { title: `In progress ${n}`, tags: ['0.4.5'] })),
    ...Array.from({ length: 12 }, (_, i) => at(60 + i, 4, { title: `Done ${60 + i}` })),
  ]
}
const tabs = () => screen.getAllByRole('tab')
// a column's count as the screen shows it: its tab on a phone, its header on a desktop
function countOf(name: string) {
  if (innerWidth <= 640) return screen.getByRole('tab', { name: new RegExp(name) }).querySelector('.count')!.textContent
  const lane = [...document.querySelectorAll<HTMLElement>('.column')].find(l => l.querySelector('h3')!.textContent === name)!
  return lane.querySelector('header .count')!.textContent
}
const pickedLane = () => document.querySelector<HTMLElement>('.column.picked')!
function swipe(el: HTMLElement, dx: number, dy: number) {
  const box = el.getBoundingClientRect()
  const x = box.left + box.width / 2, y = box.top + 100
  const t = (cx: number, cy: number) => new Touch({ identifier: 1, target: el, clientX: cx, clientY: cy })
  el.dispatchEvent(new TouchEvent('touchstart', { bubbles: true, touches: [t(x, y)], changedTouches: [t(x, y)] }))
  el.dispatchEvent(new TouchEvent('touchend', { bubbles: true, touches: [], changedTouches: [t(x + dx, y + dy)] }))
}
function open(path: string) {
  history.replaceState({}, '', path)
  const host = document.createElement('div'); host.id = 'root'; document.body.append(host)
  return render(<App />, { container: host })
}
function noOverflow() { expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth) }
function touch(target: HTMLElement) {
  const box = target.getBoundingClientRect()
  expect(box.width).toBeGreaterThanOrEqual(44)
  expect(box.height).toBeGreaterThanOrEqual(44)
}
async function sendMessage(scope: HTMLElement) {
  const box = await within(scope).findByRole('textbox')
  act(() => chat.replayDone())
  fireEvent.change(box, { target: { value: 'Please check the mobile layout.' } })
  const send = within(scope).getByRole('button', { name: 'Send' })
  await waitFor(() => expect(send.hasAttribute('disabled')).toBe(false))
  if (innerWidth === 390) touch(send)
  await userEvent.click(send)
  await waitFor(() => expect(chat.sent).toEqual(['Please check the mobile layout.']))
  const rect = box.getBoundingClientRect()
  expect(rect.width).toBeGreaterThan(240)
  expect(rect.bottom).toBeLessThanOrEqual(innerHeight)
}
for (const width of [390, 3840]) describe(`Board at ${width}px`, () => {
  beforeEach(async () => { await page.viewport(width, width === 390 ? 844 : 2160) })
  it('keeps the board and navigation in the viewport with reachable columns', async () => {
    const { container } = open('/boards/b1')
    await screen.findByText(card.title)
    const lanes = [...container.querySelectorAll<HTMLElement>('.column')]
    expect(lanes).toHaveLength(5)
    const grid = container.querySelector<HTMLElement>('.board')!
    expect(grid.scrollWidth).toBeLessThanOrEqual(grid.clientWidth)
    noOverflow()
    if (width === 390) {
      // one column at a time: the others are a tab away
      expect(lanes[0].getBoundingClientRect().width).toBeGreaterThan(300)
      expect(lanes[1].getBoundingClientRect().height).toBe(0)
      await userEvent.click(screen.getByRole('tab', { name: /Ready/ }))
      expect(lanes[1].getBoundingClientRect().width).toBeGreaterThan(300)
      expect(lanes[0].getBoundingClientRect().height).toBe(0)
      for (const t of tabs()) touch(t)
      touch(screen.getByRole('button', { name: /Board session/ }))
      for (const a of container.querySelectorAll<HTMLElement>('.topbar nav a, .topbar nav button')) touch(a)
    } else expect(lanes[1].getBoundingClientRect().left).toBeGreaterThan(lanes[0].getBoundingClientRect().left)
  })
  it('keeps a card number on one line beside a long title', async () => {
    open('/boards/b1')
    const num = await screen.findByText('#1', { selector: '.card .num' })
    const lineHeight = parseFloat(getComputedStyle(num).lineHeight) || parseFloat(getComputedStyle(num).fontSize) * 1.6
    // "#" and the digits must never break onto separate lines, even when the title wraps.
    expect(num.getBoundingClientRect().height).toBeLessThan(lineHeight * 1.5)
  })

  it('opens a full-screen phone sheet and keeps the desktop split view', async () => {
    open('/boards/b1/cards/1')
    const sheet = await screen.findByRole('dialog', { name: 'Card #1' })
    await within(sheet).findByRole('heading', { name: card.title })
    await waitFor(() => expect(sheet.classList.contains('with-session')).toBe(true))
    expect(sheet.scrollWidth).toBeLessThanOrEqual(sheet.clientWidth)
    noOverflow()
    const box = sheet.getBoundingClientRect()
    if (width === 390) {
      await waitFor(() => {
        const settled = sheet.getBoundingClientRect()
        expect(settled.x).toBe(0); expect(settled.y).toBe(0)
        expect(settled.width).toBe(390); expect(settled.height).toBe(844)
      })
      touch(within(sheet).getByRole('button', { name: 'Close' }))
      const link = within(sheet).getByRole('link', { name: /Session/ }); touch(link)
      await userEvent.click(link)
      expect(location.pathname).toBe('/boards/b1/cards/1/session')
    } else {
      expect(box.width).toBeGreaterThanOrEqual(1400)
      expect(getComputedStyle(sheet.querySelector('.sheet-session')!).display).not.toBe('none')
    }
  })
  it('gives the card session a visible, usable composer', async () => {
    const { container } = open('/boards/b1/cards/1/session')
    await screen.findByText(card.title)
    await sendMessage(container)
    noOverflow()
    const chatBox = container.querySelector('.session-chat')!.getBoundingClientRect()
    expect(chatBox.height).toBeGreaterThan(width === 390 ? 400 : 600)
    // The page grows with the screen: no fixed cap on a large monitor.
    const pageBox = container.querySelector('.board-session-page')!.getBoundingClientRect()
    if (width === 3840) expect(pageBox.width).toBeGreaterThan(2400)
    else expect(pageBox.width).toBeLessThanOrEqual(390)
  })
  it('fits the bare-repo prompt in board settings', async () => {
    boardOverride = { repos: ['a-folder-with-a-fairly-long-name'], automation_token_set: true, automation_account_id: 'owner-fixture' }
    open('/boards/b1')
    await screen.findByText(card.title)
    await userEvent.click(screen.getByRole('button', { name: /model/i }))
    const pop = await screen.findByRole('dialog', { name: 'Board settings' })
    const pick = await within(pop).findByRole('combobox', { name: 'Desktop for a-folder-with-a-fairly-long-name' })
    fireEvent.change(pick, { target: { value: 'desktop-a' } })
    await userEvent.click(within(pop).getByRole('button', { name: 'Look up a-folder-with-a-fairly-long-name' }))
    const save = await within(pop).findByRole('button', { name: 'Save example-org/a-folder-with-a-fairly-long-name' })
    noOverflow()
    for (const el of [pop, pick, save]) {
      const box = el.getBoundingClientRect()
      expect(box.left).toBeGreaterThanOrEqual(0)
      expect(box.right).toBeLessThanOrEqual(innerWidth)
    }
    expect(pop.scrollWidth).toBeLessThanOrEqual(pop.clientWidth + 1)
  })
  it('makes the board session usable without narrowing the phone composer', async () => {
    open('/boards/b1')
    await screen.findByText(card.title)
    boardSessions = [{ ...session, role: 'board' }]
    await userEvent.click(screen.getByRole('button', { name: /Board session/ }))
    const drawer = await screen.findByRole('dialog', { name: 'Board session' })
    await sendMessage(drawer)
    noOverflow()
    if (width === 390) {
      const box = drawer.getBoundingClientRect()
      expect(box.width).toBe(390); expect(box.height).toBe(844)
    }
  })
})

for (const width of [390, 3840]) describe(`Busy board at ${width}px (#124)`, () => {
  beforeEach(async () => { await page.viewport(width, width === 390 ? 844 : 2160); busyBoard() })

  it('searches from the toolbar: the 🔍 opens into a field that filters in place and lists cards to open', async () => {
    const { container } = open('/boards/b1')
    await screen.findByText(/Inbox card 40/)
    const toolbar = container.querySelector<HTMLElement>('.board-toolbar')!
    const metrics = screen.getByRole('button', { name: /Metrics/ })
    // the old full-width filter bar and chip strip are gone
    expect(container.querySelector('.board-filter, .filter-chips, .filter-chip')).toBeNull()
    expect(screen.queryByRole('button', { name: /^Tag / })).toBeNull()
    const glass = screen.getByRole('button', { name: 'Search cards' })
    expect(toolbar.contains(glass)).toBe(true)
    expect(toolbar.contains(screen.getByRole('button', { name: 'Filter' }))).toBe(true)
    if (width === 390) { touch(glass); touch(screen.getByRole('button', { name: 'Filter' })) }
    await userEvent.click(glass)
    const field = await screen.findByRole('combobox', { name: 'Search cards' })
    await waitFor(() => expect(document.activeElement).toBe(field))
    const frame = field.closest<HTMLElement>('.tb-search')!
    if (width === 3840) {
      // in the toolbar row, the height and frame of its buttons, and wide enough to read
      expect(toolbar.contains(frame)).toBe(true)
      expect(Math.abs(frame.getBoundingClientRect().height - metrics.getBoundingClientRect().height)).toBeLessThanOrEqual(1)
      expect(Math.abs(frame.getBoundingClientRect().top - metrics.getBoundingClientRect().top)).toBeLessThanOrEqual(1)
      expect(getComputedStyle(frame).borderTopLeftRadius).toBe(getComputedStyle(metrics).borderTopLeftRadius)
      expect(field.getBoundingClientRect().width).toBeGreaterThan(400)
    } else {
      // a phone: full width under the column tabs
      const tabsBox = container.querySelector<HTMLElement>('.column-tabs')!.getBoundingClientRect()
      expect(container.querySelector('.board-sticky')!.contains(frame)).toBe(true)
      expect(frame.getBoundingClientRect().top).toBeGreaterThanOrEqual(tabsBox.bottom)
      expect(frame.getBoundingClientRect().width).toBeGreaterThan(360)
      touch(field)
    }
    await userEvent.type(field, '#7')
    await waitFor(() => expect([...container.querySelectorAll('.card .num')].map(n => n.textContent)).toEqual(['#7']))
    await waitFor(() => expect(countOf('Inbox')).toBe('1/40'))
    expect(countOf('Doing')).toBe('0/3')
    const list = await screen.findByRole('listbox')
    const listBox = list.closest<HTMLElement>('.search-pop')!.getBoundingClientRect()
    expect(listBox.top).toBeGreaterThanOrEqual(frame.getBoundingClientRect().bottom)
    expect(listBox.left).toBeGreaterThanOrEqual(0); expect(listBox.right).toBeLessThanOrEqual(innerWidth)
    expect(listBox.width).toBeGreaterThan(width === 3840 ? 1000 : 370)
    expect(within(list).getAllByRole('option')[0].textContent).toContain('#7')
    noOverflow()
    // ↓ and Enter open the card, as the 🔍 search did
    await userEvent.keyboard('{ArrowDown}{Enter}')
    await screen.findByRole('dialog', { name: 'Card #7' })
    expect(location.pathname).toBe('/boards/b1/cards/7')
  })

  it('opens the filter choices from a Filter button and shows only what is on', async () => {
    const { container } = open('/boards/b1')
    await screen.findByText(/Inbox card 40/)
    const toolbar = container.querySelector<HTMLElement>('.board-toolbar')!
    const rowHeight = toolbar.getBoundingClientRect().height
    await userEvent.click(screen.getByRole('button', { name: 'Filter' }))
    const pop = await screen.findByRole('dialog', { name: 'Filter cards' })
    for (const section of ['Type', 'Priority', 'Tag']) expect(within(pop).getByRole('group', { name: section })).toBeTruthy()
    const box = pop.getBoundingClientRect()
    expect(box.left).toBeGreaterThanOrEqual(0); expect(box.right).toBeLessThanOrEqual(innerWidth)
    if (width === 390) {
      // a bottom sheet across the phone
      const sheet = pop.getBoundingClientRect()
      expect(sheet.width).toBe(390); expect(Math.round(sheet.bottom)).toBe(844)
      expect(pop.getAttribute('aria-modal')).toBe('true')
    } else {
      expect(box.top).toBeGreaterThanOrEqual(screen.getByRole('button', { name: 'Filter' }).getBoundingClientRect().bottom)
      expect(box.width).toBeGreaterThan(1200) // uses the space on a large screen
    }
    await userEvent.click(within(pop).getByRole('button', { name: 'Tag 0.4.5, 7 cards' }))
    await userEvent.click(within(pop).getByRole('button', { name: 'Type feature, 4 cards' }))
    await waitFor(() => expect(countOf('Inbox')).toBe('4/40'))
    if (width === 390) await userEvent.click(within(pop).getByRole('button', { name: 'Done' }))
    else await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Filter cards' })).toBeNull())
    const active = screen.getByRole('group', { name: 'Active filters' })
    expect([...active.querySelectorAll('button')].map(b => b.textContent)).toEqual(['type: feature ×', '0.4.5 ×', 'Clear'])
    if (width === 3840) {
      // still one toolbar row: the chips sit beside the Filter button
      expect(toolbar.contains(active)).toBe(true)
      expect(toolbar.getBoundingClientRect().height).toBeLessThanOrEqual(rowHeight + 1)
    } else {
      expect(container.querySelector('.board-sticky')!.contains(active)).toBe(true)
      for (const b of active.querySelectorAll<HTMLElement>('button')) touch(b)
    }
    await userEvent.click(screen.getByRole('button', { name: 'Remove type: feature' }))
    await waitFor(() => expect(countOf('Doing')).toBe('3/3'))
    await userEvent.click(screen.getByRole('button', { name: 'Clear' }))
    await waitFor(() => expect(screen.queryByRole('group', { name: 'Active filters' })).toBeNull())
    await waitFor(() => expect(countOf('Inbox')).toBe('40'))
    noOverflow()
  })

  // Card #128: a picked choice and an active chip (and, on a phone, the picked column tab) are the
  // selected blue, never the accent (Blaze), which beside the danger red read as a warning.
  it.each(['light', 'dark'] as const)('draws a picked filter in the selected blue, not the accent (%s)', async theme => {
    document.documentElement.dataset.theme = theme
    try {
      const { container } = open('/boards/b1')
      await screen.findByText(/Inbox card 40/)
      await userEvent.click(screen.getByRole('button', { name: 'Filter' }))
      const chip = await screen.findByRole('button', { name: 'Tag 0.4.5, 7 cards' })
      await userEvent.click(chip)
      await waitFor(() => expect(chip).toHaveClass('on'))
      const probe = document.createElement('div')
      document.body.append(probe)
      const resolve = (value: string) => { probe.style.color = value; return getComputedStyle(probe).color }
      const selected = resolve('var(--selected-border)'), selectedText = resolve('var(--selected-text)')
      const accent = [resolve('var(--blaze)'), resolve('var(--blaze-soft)')]
      probe.remove()
      expect(selected).toBe(theme === 'dark' ? 'rgb(76, 155, 255)' : 'rgb(47, 111, 214)')
      for (const el of [chip, screen.getByRole('button', { name: 'Remove 0.4.5' })]) {
        const cs = getComputedStyle(el)
        expect(cs.borderTopColor).toBe(selected)
        expect(cs.color).toBe(selectedText)
        expect(accent).not.toContain(cs.borderTopColor)
        expect(accent).not.toContain(cs.backgroundColor)
      }
      if (width === 390) {
        const tab = screen.getByRole('tab', { selected: true })
        expect(getComputedStyle(tab).borderTopColor).toBe(selected)
        expect(getComputedStyle(tab.querySelector('.count')!).color).toBe(selectedText)
      } else {
        expect(getComputedStyle(container.querySelector('.column-tabs')!).display).toBe('none')
      }
    } finally {
      delete document.documentElement.dataset.theme
    }
  })

  it('ignores / while a dialog or popover is open, and opens the search when nothing is', async () => {
    open('/boards/b1')
    await screen.findByText(/Inbox card 40/)
    const noSearch = async () => {
      await new Promise(r => setTimeout(r, 150)) // let a (wrongly) taken shortcut land
      expect(screen.queryByRole('dialog', { name: 'Search cards' })).toBeNull()
      expect(document.activeElement?.getAttribute('role')).not.toBe('combobox')
    }
    // the Filter choices (a popover; a sheet on a phone)
    await userEvent.click(screen.getByRole('button', { name: 'Filter' }))
    await screen.findByRole('dialog', { name: 'Filter cards' })
    await userEvent.keyboard('/')
    await noSearch()
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Filter cards' })).toBeNull())
    // Board settings (a popover dialog), with focus on the page behind it
    await userEvent.click(screen.getByRole('button', { name: /model/i }))
    await screen.findByRole('dialog', { name: 'Board settings' })
    ;(document.activeElement as HTMLElement | null)?.blur()
    await userEvent.keyboard('/')
    await noSearch()
    await userEvent.click(screen.getByRole('button', { name: /model/i }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Board settings' })).toBeNull())
    // nothing open: / opens the board's own field
    ;(document.activeElement as HTMLElement | null)?.blur()
    await userEvent.keyboard('/')
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('combobox', { name: 'Search cards' })))
    expect(screen.queryByRole('dialog', { name: 'Search cards' })).toBeNull()
  })

  if (width === 3840) {
    it('keeps the board filling the window, every column side by side, none collapsible', async () => {
      const { container } = open('/boards/b1')
      await screen.findByText(/Inbox card 40/)
      const lanes = [...container.querySelectorAll<HTMLElement>('.column')]
      for (const lane of lanes) expect(lane.getBoundingClientRect().height).toBeGreaterThan(0)
      for (const lane of lanes) expect(lane.querySelectorAll('header button, header [aria-expanded]')).toHaveLength(0)
      const board = container.querySelector<HTMLElement>('.board')!.getBoundingClientRect()
      expect(board.bottom).toBeGreaterThan(2160 - 40)
      expect(document.documentElement.scrollHeight).toBeLessThanOrEqual(2160)
    })
  }

  if (width === 390) {
    it('shows one column at a time under sticky tabs that scroll sideways', async () => {
      const { container } = open('/boards/b1')
      await screen.findByText(/Inbox card 40/)
      expect(tabs().map(t => t.textContent)).toEqual(['Inbox40', 'Ready0', 'Doing3', 'Review0', 'Done12'])
      expect(pickedLane().querySelectorAll('.card')).toHaveLength(40)
      const strip = container.querySelector<HTMLElement>('.column-tabs')!
      expect(strip.scrollWidth).toBeGreaterThan(strip.clientWidth) // five tabs do not fit: the strip scrolls
      noOverflow()
      // deep in the Inbox the tabs and the filter stay at the top of the screen
      scrollTo(0, 3000)
      await waitFor(() => expect(scrollY).toBeGreaterThan(2000))
      const sticky = container.querySelector<HTMLElement>('.board-sticky')!.getBoundingClientRect()
      expect(sticky.top).toBe(0)
      expect(sticky.height).toBeLessThan(844 / 4)
      // picking the last tab shows Done from its top and brings the tab into view
      const done = screen.getByRole('tab', { name: /Done/ })
      await userEvent.click(done)
      expect(done.getAttribute('aria-selected')).toBe('true')
      expect(pickedLane().querySelector('h3')!.textContent).toBe('Done')
      await waitFor(() => expect(done.getBoundingClientRect().right).toBeLessThanOrEqual(391))
      expect(pickedLane().getBoundingClientRect().top).toBeGreaterThanOrEqual(sticky.height - 1)
      expect(localStorage.getItem('blerg-board:column:b1')).toBe('col-4')
    })

    it('closes the phone search row with an empty field: Escape, or ×', async () => {
      open('/boards/b1')
      await screen.findByText(/Inbox card 40/)
      await userEvent.click(screen.getByRole('button', { name: 'Search cards' }))
      const field = await screen.findByRole('combobox', { name: 'Search cards' })
      await waitFor(() => expect(document.activeElement).toBe(field))
      await userEvent.keyboard('{Escape}') // the list
      await userEvent.keyboard('{Escape}') // the row
      await waitFor(() => expect(screen.queryByRole('combobox', { name: 'Search cards' })).toBeNull())
      await userEvent.click(screen.getByRole('button', { name: 'Search cards' }))
      await screen.findByRole('combobox', { name: 'Search cards' })
      await userEvent.type(screen.getByRole('combobox', { name: 'Search cards' }), 'card')
      const close = screen.getByRole('button', { name: 'Close search' })
      touch(close)
      await userEvent.click(close)
      await waitFor(() => expect(screen.queryByRole('combobox', { name: 'Search cards' })).toBeNull())
      expect(countOf('Inbox')).toBe('40')
    })

    it('switches columns in place: no card flies in, and the shown column is a named tab panel', async () => {
      open('/boards/b1')
      await screen.findByText(/Inbox card 40/)
      await new Promise(r => requestAnimationFrame(() => r(null)))
      const animate = vi.spyOn(Element.prototype, 'animate')
      try {
        await userEvent.click(screen.getByRole('tab', { name: /Done/ }))
        const panel = await screen.findByRole('tabpanel', { name: /Done/ })
        expect(panel.tabIndex).toBe(0)
        panel.focus()
        expect(document.activeElement).toBe(panel)
        await new Promise(r => requestAnimationFrame(() => r(null)))
        expect(animate).not.toHaveBeenCalled()
        swipe(document.querySelector<HTMLElement>('.board')!, 150, 0)
        await screen.findByRole('tabpanel', { name: /Review/ })
        expect(animate).not.toHaveBeenCalled()
      } finally { animate.mockRestore() }
    })

    it('swipes between columns without stealing vertical scrolls', async () => {
      const { container } = open('/boards/b1')
      await screen.findByText(/Inbox card 40/)
      const board = container.querySelector<HTMLElement>('.board')!
      // the browser keeps every pan (a zoomed-in person can pan sideways)
      expect(getComputedStyle(board).touchAction).toBe('auto')
      // pinch-zoomed in, a sideways drag pans the page: not a swipe
      Object.defineProperty(visualViewport!, 'scale', { configurable: true, get: () => 2 })
      try {
        swipe(board, -150, 10)
        await new Promise(r => setTimeout(r, 50))
        expect(pickedLane().querySelector('h3')!.textContent).toBe('Inbox')
      } finally { delete (visualViewport as unknown as Record<string, unknown>).scale }
      swipe(board, -150, 10)
      await waitFor(() => expect(pickedLane().querySelector('h3')!.textContent).toBe('Ready'))
      swipe(board, -150, 5)
      await waitFor(() => expect(pickedLane().querySelector('h3')!.textContent).toBe('Doing'))
      swipe(board, 20, -300) // a vertical scroll
      swipe(board, -30, 0) // a tap that wobbled
      expect(pickedLane().querySelector('h3')!.textContent).toBe('Doing')
      swipe(board, 150, 0)
      await waitFor(() => expect(pickedLane().querySelector('h3')!.textContent).toBe('Ready'))
      swipe(board, 150, 0); swipe(board, 150, 0) // past the first column: stays on it
      await waitFor(() => expect(pickedLane().querySelector('h3')!.textContent).toBe('Inbox'))
    })

    it('remembers the picked column for the board', async () => {
      localStorage.setItem('blerg-board:column:b1', 'col-2')
      open('/boards/b1')
      await screen.findByText('In progress 41')
      expect(screen.getByRole('tab', { name: /Doing/ }).getAttribute('aria-selected')).toBe('true')
      expect(pickedLane().querySelectorAll('.card')).toHaveLength(3)
      expect(screen.getByText(/Inbox card 40/).getBoundingClientRect().height).toBe(0)
    })
  }
})

// About 100 distinct tags: the toolbar stays one row, and the choices scroll in their own box.
for (const [width, height] of [[1280, 720], [390, 844]]) describe(`Board with ~100 tags at ${width}x${height} (#124, #141)`, () => {
  beforeEach(async () => {
    await page.viewport(width, height); busyBoard()
    boardCards = boardCards.map((c, k) => ({ ...c, tags: [`area-${k * 2}`, `area-${k * 2 + 1}`].filter((_, j) => k * 2 + j < 100) }))
  })
  it('keeps the tags in the Filter choices, which scroll and can be searched', async () => {
    const { container } = open('/boards/b1')
    await screen.findByText(/Inbox card 40/)
    const board = () => container.querySelector<HTMLElement>('.board')!.getBoundingClientRect()
    expect(screen.queryAllByRole('button', { name: /^Tag / })).toHaveLength(0) // nothing until asked
    if (width === 1280) expect(board().height).toBeGreaterThanOrEqual(height / 2)
    noOverflow()
    await userEvent.click(screen.getByRole('button', { name: 'Filter' }))
    const pop = await screen.findByRole('dialog', { name: 'Filter cards' })
    const tags = within(pop).getByRole('group', { name: 'Tag' })
    expect(within(tags).getAllByRole('button')).toHaveLength(100)
    const box = pop.getBoundingClientRect()
    expect(box.top).toBeGreaterThanOrEqual(0); expect(box.bottom).toBeLessThanOrEqual(height + 1)
    const scroller = width === 390 ? pop.querySelector<HTMLElement>('.filter-sheet-body')! : tags
    expect(scroller.scrollHeight).toBeGreaterThan(scroller.clientHeight)
    await userEvent.type(within(pop).getByRole('searchbox', { name: 'Find a tag' }), 'area-9')
    await waitFor(() => expect(within(tags).getAllByRole('button')).toHaveLength(11)) // area-9, area-90 … 99
    noOverflow()
  })
})

// Between the phone and a wide screen the toolbar's search, Filter and active chips stay together:
// on their own row under the toolbar up to 1000 px, the search framed like the toolbar's buttons.
for (const width of [641, 800, 1000, 1280]) describe(`Toolbar search at ${width}px (#141)`, () => {
  beforeEach(async () => { await page.viewport(width, 900); busyBoard() })
  it('keeps the field, Filter and the chips in one tidy group', async () => {
    sessionStorage.setItem('blerg-board:filter:b1', JSON.stringify({ q: 'card', tag: ['0.4.5'], type: ['feature'], priority: [] }))
    const { container } = open('/boards/b1')
    await screen.findByText(/Inbox card 1 /)
    const field = screen.getByRole('combobox', { name: 'Search cards' })
    const frame = field.closest<HTMLElement>('.tb-search')!.getBoundingClientRect()
    const metrics = screen.getByRole('button', { name: /Metrics/ })
    const filterBtn = screen.getByRole('button', { name: /^Filter/ }).getBoundingClientRect()
    expect(Math.abs(frame.height - metrics.getBoundingClientRect().height)).toBeLessThanOrEqual(1)
    expect(getComputedStyle(field.closest('.tb-search')!).borderTopColor).toBe(getComputedStyle(metrics).borderTopColor)
    expect(Math.abs(frame.top - filterBtn.top)).toBeLessThanOrEqual(1) // never split across rows
    const group = container.querySelector<HTMLElement>('.tb-find')!.getBoundingClientRect()
    if (width <= 1000) {
      // its own row, starting under the board's name
      const title = container.querySelector<HTMLElement>('.board-toolbar h1')!.getBoundingClientRect()
      expect(Math.abs(group.left - title.left)).toBeLessThanOrEqual(1)
      expect(group.top).toBeGreaterThanOrEqual(metrics.getBoundingClientRect().bottom - 1)
    }
    const chips = [...container.querySelectorAll<HTMLElement>('.active-filters button')]
    expect(chips.map(c => c.textContent)).toEqual(['search: card ×', 'type: feature ×', '0.4.5 ×', 'Clear'])
    for (const c of chips) {
      const b = c.getBoundingClientRect()
      expect(b.left).toBeGreaterThanOrEqual(group.left - 1); expect(b.right).toBeLessThanOrEqual(innerWidth)
    }
    // "Clear" never sits alone on a row
    const clear = chips[chips.length - 1].getBoundingClientRect(), before = chips[chips.length - 2].getBoundingClientRect()
    expect(Math.abs(clear.top - before.top)).toBeLessThanOrEqual(2)
    noOverflow()
  })
})
