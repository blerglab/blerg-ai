import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import SessionList from './SessionList'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import '../index.css'
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn(async () => ({ ok: true, status: 200, json: async () => ({}) })) }))
afterEach(() => localStorage.clear())

const child = (id: string, title: string, status: 'idle' | 'running' | 'waiting') =>
  makeSession({ id, title, status, parent_session_id: 'parent', started_by: { kind: 'session', name: 'Parent' } })

// The started sessions' control is a small chevron in the card's top line (44 px to touch on a
// phone, without looking bigger), no extra row between cards; collapsed, the children that run or need the person stay, inside the sidebar.
describe('started sessions in the sidebar, in Chromium', () => {
  it.each([390, 3840])('at %i px', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    const sessions = [makeSession({ id: 'parent', title: 'A parent session with a long enough title to wrap' }), child('a', 'Idle worker', 'idle'), child('b', 'Asking worker', 'waiting'), child('c', 'Busy worker', 'running'), makeSession({ id: 'solo', title: 'A parent session with a long enough title to wrap' })]
    useSessionStore.setState({ daemons: [makeDaemon()], sessions, statusChangedAt: Object.fromEntries(sessions.map(s => [s.id, Date.now()])), serverVersion: 'test' })
    render(<MemoryRouter><div style={{ width: width === 3840 ? 420 : '100%' }}><SessionList variant="sidebar" /></div></MemoryRouter>)
    const toggle = screen.getByRole('button', { name: 'Hide 3 started sessions' })
    const cardEl = document.querySelector('[data-session-branch="parent"] > div')!
    const card = cardEl.getBoundingClientRect()
    const box = toggle.getBoundingClientRect()
    const solo = document.querySelector('[data-session-branch="solo"] > div')!.getBoundingClientRect()
    const inside = (b: DOMRect, c: DOMRect) => b.left >= c.left && b.right <= c.right && b.top >= c.top && b.bottom <= c.bottom
    expect(box.height).toBeLessThanOrEqual(20)
    expect(document.querySelector('[data-testid="children-bar"]')).toBeNull()
    expect(document.querySelector('.session-children-anchor')).toBeNull()
    // Inside the parent card, in both states; the line drops straight down from its center.
    expect(inside(box, card)).toBe(true)
    const line = document.querySelector('[data-testid="nested-sessions"]')!.getBoundingClientRect()
    expect(Math.abs(box.left + box.width / 2 - (line.left + 0.5))).toBeLessThanOrEqual(1)
    expect(box.bottom).toBeLessThanOrEqual(line.top)
    // No taller than a card without started sessions.
    expect(card.height).toBeLessThanOrEqual(solo.height + 1)
    // On a phone the hit area reaches 44 px wide around the small button, within the card.
    if (width === 390) {
      const c = { x: box.left + box.width / 2, y: box.top + box.height / 2 }
      for (const [dx, dy] of [[-21, 0], [21, 0], [0, -13], [0, 13]]) expect(document.elementFromPoint(c.x + dx, c.y + dy)).toBe(toggle)
    }
    // The card's own controls stay reachable: its right-hand end is not under the hit area.
    const right = document.elementFromPoint(card.right - 14, card.top + 14)
    expect(right).not.toBe(toggle)
    expect(cardEl.contains(right)).toBe(true)
    await userEvent.click(toggle)
    expect(screen.getByRole('button', { name: 'Show 3 started sessions' })).toHaveAttribute('aria-expanded', 'false')
    // Collapsed: still no extra height; the next card (here the solo one) starts where the kept rows end.
    // Collapsed: same spot, still inside the card, and the card is no taller.
    const collapsed = document.querySelector('[data-session-branch="parent"] > div')!.getBoundingClientRect()
    const box2 = screen.getByRole('button', { name: 'Show 3 started sessions' }).getBoundingClientRect()
    expect(inside(box2, collapsed)).toBe(true)
    expect(Math.abs(box2.left - box.left)).toBeLessThanOrEqual(1)
    expect(collapsed.height).toBeLessThanOrEqual(solo.height + 1)
    expect(screen.getByTestId('hidden-children').getAttribute('aria-label')).toBe('1 needs you, 1 running, hidden (3 started sessions)')
    const kept = screen.getAllByTestId('kept-child')
    expect(kept).toHaveLength(2)
    for (const k of kept) {
      const r = k.getBoundingClientRect()
      expect(r.right).toBeLessThanOrEqual(width)
      if (width === 390) expect(r.height).toBeGreaterThanOrEqual(44)
    }
    expect(screen.queryByText('Idle worker')).toBeNull()
  })
})
