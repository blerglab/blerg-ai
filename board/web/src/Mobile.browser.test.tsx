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
const card = { id: 'c1', board_id: 'b1', number: 1, rank: 'a', column_id: 'col-0', title: 'A card with a long title that should wrap comfortably on a phone', type: 'bug', priority: 'normal', body: '## Context\n\n' + 'LongUnbrokenReference'.repeat(12), fields: {}, repos: ['example-org/example-repo'], tags: [], links: [], depends_on: [], blockers: [], model: '', version: 1, auto_merge: false, run_attempts: 0, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' }
let boardSessions: typeof session[] = []
const locationBefore = location.href
vi.mock('./api', async original => ({
  ...await original<typeof import('./api')>(),
  boardSocket: () => () => {},
  api: vi.fn(async (path: string) => {
    if (path === '/api/me') return { is_admin: true, is_human: true, kind: 'human' }
    if (path === '/api/version') return { release: '0.4.4', commit: 'fixture' }
    if (path === '/api/boards/b1') return board
    if (path === '/api/boards/b1/columns') return columns
    if (path === '/api/boards/b1/cards') return [card]
    if (path === '/api/cards/c1') return card
    if (path === '/api/cards/c1/runner-sessions') return [session]
    if (path === '/api/boards/b1/sessions') return boardSessions
    if (path === '/api/boards/b1/run') return { state: 'stopped', ready: 0, blocked: 0, in_flight: 0 }
    if (path === '/api/cards/c1/run-targets') return { can_choose: false, targets: [] }
    return []
  }),
}))
vi.mock('@blerglab/chat', async original => ({ ...await original<typeof import('@blerglab/chat')>(), createProxyTransport: () => chat.transport }))

beforeEach(() => { resetChatState(); boardSessions = []; chat.sent.length = 0; localStorage.clear(); sessionStorage.clear() })
afterEach(() => { cleanup(); history.replaceState({}, '', locationBefore) })
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
      expect(lanes[1].getBoundingClientRect().top).toBeGreaterThan(lanes[0].getBoundingClientRect().bottom - 1)
      touch(screen.getByRole('button', { name: /Board session/ }))
      for (const a of container.querySelectorAll<HTMLElement>('.topbar nav a, .topbar nav button')) touch(a)
    } else expect(lanes[1].getBoundingClientRect().left).toBeGreaterThan(lanes[0].getBoundingClientRect().left)
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
