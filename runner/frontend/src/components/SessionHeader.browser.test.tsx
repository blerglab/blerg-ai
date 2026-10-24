// The session header's controls in a real browser (card #87): one row of one icon-button style at a
// phone's width and on a 4K screen, the ⋯ menu inside the screen, and the app's one bell (in the
// sessions header) the same size as the header's buttons.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import '../index.css'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'

vi.mock('../ws', () => import('../test/wsMock'))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

import SessionDetail from './SessionDetail'
import SessionList from './SessionList'

const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }))

beforeEach(() => {
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
  it.each([390, 3840])('fits its controls on one row, in one style, at %i px', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    mount(width > 1000)
    const group = await screen.findByTestId('session-header-controls')
    const buttons = [...group.querySelectorAll<HTMLButtonElement>('button:not([role="menuitem"])')]
    expect(buttons).toHaveLength(width < 900 ? 6 : 7)
    expect(group.querySelector('[aria-label="Search commands and files"]')).not.toBeNull()
    // Exactly one Notifications bell on the page: in the header on a phone (no sidebar there), in
    // the sidebar beside + New on a wide screen.
    const bells = screen.getAllByRole('button', { name: /^Notifications/ })
    expect(bells).toHaveLength(1)
    expect(group.contains(bells[0])).toBe(width < 900)
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
      if (b.dataset.testid !== 'rail-toggle') expect(rects[i].width).toBe(rects[0].width)
      expect(b.querySelector('svg')!.getAttribute('stroke-width') ?? b.querySelector('svg')!.getAttribute('strokeWidth')).toBe('1.75')
    }
    // The title keeps room.
    const title = group.parentElement!.querySelector<HTMLElement>('div[style*="cursor: text"]')!
    expect(title.textContent).toMatch(/Ship the release notes/)
    expect(title.getBoundingClientRect().width).toBeGreaterThan(width > 1000 ? 600 : 100)
    // The ⋯ menu opens inside the screen.
    await userEvent.click(screen.getByRole('button', { name: 'More actions' }))
    const menu = screen.getByRole('menu', { name: 'More actions' }).getBoundingClientRect()
    expect(menu.left).toBeGreaterThanOrEqual(0)
    expect(menu.right).toBeLessThanOrEqual(width)
    expect(screen.getByRole('menuitem', { name: 'Discard session and children' })).toBeVisible()
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
      // End session stays the only red control.
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
