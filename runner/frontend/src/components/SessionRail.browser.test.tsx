// The session page's side panel in real Chromium: whether the toggle fits a phone's header, the
// drawer opens over the whole height and closes, and nothing is wider than the screen; and on a
// desktop, that the panel is a column beside the chat that the chat and its composer give way to.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import '../index.css'
import { seedRichRail } from '../test/railFixture'
import { reelReply } from '../test/reelFixture'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async (orig) => ({
  ...(await orig<typeof import('../authClient')>()),
  getAccessToken: () => 'tok',
  // Never let a test navigate the test page itself.
  redirectToRefresh: vi.fn(),
}))
// The chat's transport reads the files with that token, taken as fresh.
vi.mock('../claims', async (orig) => ({ ...(await orig<typeof import('../claims')>()), tokenExpired: () => false }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

import SessionDetail from './SessionDetail'

const PHONE = { width: 390, height: 700 }
const DESKTOP = { width: 1280, height: 800 }

const FILES = [
  { id: 'f2', name: 'a-rather-long-file-name-for-the-release-plan-of-the-session-store.md', size: 6144, content_type: 'text/markdown', view: 'markdown', version: 2, latest_version: 2 },
  { id: 'f1', name: 'a-rather-long-file-name-for-the-release-plan-of-the-session-store.md', size: 4096, content_type: 'text/markdown', view: 'markdown', version: 1, latest_version: 2 },
  { id: 'f3', name: 'coverage.csv', size: 18432, content_type: 'text/csv', view: 'csv', version: 1, latest_version: 1 },
]
const CAPS = {
  seq: 3, ts: '2026-10-15T10:00:00Z',
  capabilities: { engine: 'claude', groups: [{ id: 'skills', label: 'Skills', items: [{ name: 'release-notes', description: 'Writes release notes from the merged pull requests since the last tag.' }] }] },
}

const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }))

beforeEach(() => {
  localStorage.removeItem(RAIL_PREF_KEY)
  localStorage.removeItem(RAIL_TAB_KEY)
  const { live } = seedRichRail()
  apiFetch.mockImplementation((url: string) => json(
    url.startsWith('/api/tool-changes') ? { tool_changes: [{ id: 't1', kind: 'changed', muted: false }] }
      : url.endsWith('/topics') ? reelReply()
        : url.endsWith('/live') ? live
          : url.endsWith('/capabilities') ? CAPS
            : {},
  ))
  vi.stubGlobal('fetch', vi.fn((url: string) => json(String(url).endsWith('/artifacts') ? { artifacts: FILES } : [])))
})
afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.removeItem(RAIL_PREF_KEY)
  localStorage.removeItem(RAIL_TAB_KEY)
})

function renderPage() {
  render(
    <div style={{ height: '100vh' }}>
      <MemoryRouter initialEntries={['/sessions/sess-a']}>
        <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
      </MemoryRouter>
    </div>,
  )
}

const noOverflow = (width: number) => {
  expect(document.documentElement.clientWidth).toBe(width)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
}

describe('the session panel on a phone (390 px)', () => {
  beforeEach(async () => { await page.viewport(PHONE.width, PHONE.height) })

  it('the toggle fits the header with its count, and the page is no wider than the screen', async () => {
    renderPage()
    const toggle = screen.getByTestId('rail-toggle')
    // 1 open ask + 1 tool change need you; 2 subagents (A1 at work in a running turn, A2 in the
    // background, its task counted once) + 5 background tasks (3 the page holds, 2 that started
    // before them) run: 9, all from the server's live state.
    await waitFor(() => expect(screen.getByTestId('rail-badge').textContent).toBe('9'))
    expect(screen.getByTestId('rail-announce').textContent).toBe('Session panel: 2 need you, 7 running')
    const r = toggle.getBoundingClientRect()
    expect(r.left).toBeGreaterThanOrEqual(0)
    expect(r.right).toBeLessThanOrEqual(PHONE.width)
    const header = toggle.parentElement!
    expect(header.scrollWidth).toBeLessThanOrEqual(header.clientWidth)
    // Closed, there is no drawer, and the chat has the whole width.
    expect(screen.queryByTestId('session-rail')).toBeNull()
    expect(screen.getByTestId('agent-chat').getBoundingClientRect().width).toBeGreaterThan(PHONE.width - 2)
    noOverflow(PHONE.width)
  })

  it('opens a full-height drawer from the toggle that Escape and the backdrop close', async () => {
    renderPage()
    const toggle = screen.getByTestId('rail-toggle')
    await userEvent.click(toggle)
    const drawer = await screen.findByRole('dialog', { name: 'Session panel' })
    // It slides in from the right edge; measured once it is in place.
    await waitFor(() => expect(drawer.getBoundingClientRect().right).toBe(PHONE.width))
    const d = drawer.getBoundingClientRect()
    expect(d.top).toBe(0)
    expect(d.bottom).toBe(PHONE.height)
    expect(d.left).toBeGreaterThan(0)
    expect(d.right).toBe(PHONE.width)
    noOverflow(PHONE.width)
    // Every row stays inside the drawer; the list scrolls inside it, not the page.
    for (const row of drawer.querySelectorAll('.rail-item')) {
      const rr = row.getBoundingClientRect()
      expect(rr.left).toBeGreaterThanOrEqual(d.left)
      expect(rr.right).toBeLessThanOrEqual(d.right)
    }
    const body = drawer.querySelector('.rail-body') as HTMLElement
    expect(body.scrollHeight).toBeGreaterThan(body.clientHeight)
    expect(getComputedStyle(document.body).overflow).toBe('hidden')

    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByTestId('session-rail')).toBeNull())
    expect(document.activeElement).toBe(toggle)
    expect(document.body.style.overflow).toBe('')

    await userEvent.click(toggle)
    await screen.findByRole('dialog', { name: 'Session panel' })
    // The strip of page left of the drawer is the backdrop.
    await userEvent.click(screen.getByTestId('session-rail-backdrop'), { position: { x: 10, y: 300 } })
    await waitFor(() => expect(screen.queryByTestId('session-rail')).toBeNull())
  })

  it('shows History in the drawer: cards as wide as the list, version chips wrapping inside them', async () => {
    localStorage.setItem(RAIL_TAB_KEY, 'history')
    renderPage()
    await userEvent.click(screen.getByTestId('rail-toggle'))
    const drawer = await screen.findByRole('dialog', { name: 'Session panel' })
    await waitFor(() => expect(drawer.getBoundingClientRect().right).toBe(PHONE.width))
    const list = await screen.findByTestId('reel-list')
    for (const head of screen.getAllByTestId('reel-head')) await userEvent.click(head)
    const pad = getComputedStyle(list)
    const inner = list.getBoundingClientRect().width - parseFloat(pad.paddingLeft) - parseFloat(pad.paddingRight)
    for (const card of screen.getAllByTestId('reel-card')) expect(Math.round(card.getBoundingClientRect().width)).toBe(Math.round(inner))
    for (const chip of screen.getAllByTestId('reel-chip')) {
      const c = chip.getBoundingClientRect()
      expect(c.right).toBeLessThanOrEqual(drawer.getBoundingClientRect().right)
    }
    noOverflow(PHONE.width)
  })

  it('shows Files in the drawer: every row and its buttons inside it, the page no wider than the screen', async () => {
    localStorage.setItem(RAIL_TAB_KEY, 'files')
    renderPage()
    // The chat's toolbar has no Files or Skills & plugins button.
    expect(screen.queryByTestId('files-toggle')).toBeNull()
    expect(screen.queryByTestId('capabilities-toggle')).toBeNull()
    await userEvent.click(screen.getByTestId('rail-toggle'))
    const drawer = await screen.findByRole('dialog', { name: 'Session panel' })
    await waitFor(() => expect(drawer.getBoundingClientRect().right).toBe(PHONE.width))
    const rows = await screen.findAllByTestId('artifact-row')
    expect(rows).toHaveLength(2)
    await userEvent.click(screen.getByRole('button', { name: /Earlier versions/ }))
    const d = drawer.getBoundingClientRect()
    for (const el of drawer.querySelectorAll('.artifact-row, .artifact-version-row, .artifact-btn')) {
      const r = el.getBoundingClientRect()
      expect(r.left).toBeGreaterThanOrEqual(d.left)
      expect(r.right).toBeLessThanOrEqual(d.right)
    }
    // The four tabs fit the drawer's width on one line.
    const tabs = screen.getAllByRole('tab')
    expect(tabs.map(t => t.id.split('-tab-')[1])).toEqual(['live', 'history', 'files', 'capabilities'])
    expect(new Set(tabs.map(t => Math.round(t.getBoundingClientRect().top))).size).toBe(1)
    expect(tabs.at(-1)!.getBoundingClientRect().right).toBeLessThanOrEqual(d.right)
    noOverflow(PHONE.width)
  })
})

describe('the session panel on a desktop (1280 px)', () => {
  beforeEach(async () => { await page.viewport(DESKTOP.width, DESKTOP.height) })

  it('is a column beside the chat, the chat and its composer give way, and the toggle hides it', async () => {
    renderPage()
    const rail = screen.getByTestId('session-rail')
    // With no choice made, the panel is open.
    await waitFor(() => expect(rail.getBoundingClientRect().width).toBeGreaterThan(0))
    const r = rail.getBoundingClientRect()
    expect(r.width).toBeGreaterThanOrEqual(320)
    expect(r.width).toBeLessThanOrEqual(360)
    expect(r.right).toBe(DESKTOP.width)
    expect(r.bottom).toBe(DESKTOP.height)
    const chat = screen.getByTestId('agent-chat').getBoundingClientRect()
    expect(chat.right).toBeLessThanOrEqual(r.left)
    const composer = screen.getByPlaceholderText('Answer the agent’s question…').getBoundingClientRect()
    expect(composer.right).toBeLessThanOrEqual(r.left)
    expect(composer.left).toBeGreaterThanOrEqual(chat.left)
    noOverflow(DESKTOP.width)

    await userEvent.click(screen.getByTestId('rail-toggle'))
    await waitFor(() => expect(rail.getBoundingClientRect().width).toBe(0))
    expect(screen.getByTestId('agent-chat').getBoundingClientRect().width).toBeGreaterThan(DESKTOP.width - 2)
    expect(localStorage.getItem(RAIL_PREF_KEY)).toBe('0')
  })

  it('Capabilities fills the panel’s column, its filter and groups inside it; Session closes Live', async () => {
    renderPage()
    const rail = screen.getByTestId('session-rail')
    await waitFor(() => expect(rail.getBoundingClientRect().width).toBeGreaterThan(0))
    const r = rail.getBoundingClientRect()
    const settings = screen.getByTestId('rail-session')
    expect(settings.getBoundingClientRect().right).toBeLessThanOrEqual(r.right)
    await userEvent.click(screen.getByRole('tab', { name: /^Capabilities/ }))
    await screen.findByText('release-notes')
    const search = screen.getByRole('searchbox', { name: 'Filter skills, plugins and tools' }).getBoundingClientRect()
    expect(search.left).toBeGreaterThanOrEqual(r.left)
    expect(search.right).toBeLessThanOrEqual(r.right)
    for (const g of screen.getAllByTestId(/^caps-group-/)) expect(g.getBoundingClientRect().right).toBeLessThanOrEqual(r.right)
    noOverflow(DESKTOP.width)
  })
})
