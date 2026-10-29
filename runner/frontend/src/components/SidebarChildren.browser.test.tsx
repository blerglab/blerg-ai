import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, within } from '@testing-library/react'
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
// phone, without looking bigger), no extra row between cards; collapsed, every child stays hidden.
describe('started sessions in the sidebar, in Chromium', () => {
  it.each([390, 3840])('at %i px', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    const sessions = [makeSession({ id: 'parent', status: 'idle', title: 'A parent session with a long enough title to wrap' }), child('a', 'Idle worker', 'idle'), child('b', 'Asking worker', 'waiting'), child('c', 'Busy worker', 'running'), makeSession({ id: 'solo', title: 'A parent session with a long enough title to wrap' })]
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
    // Collapsed: same spot, still inside the card, and the card is no taller.
    const collapsed = document.querySelector('[data-session-branch="parent"] > div')!.getBoundingClientRect()
    const box2 = screen.getByRole('button', { name: 'Show 3 started sessions' }).getBoundingClientRect()
    expect(inside(box2, collapsed)).toBe(true)
    expect(Math.abs(box2.left - box.left)).toBeLessThanOrEqual(1)
    expect(collapsed.height).toBeLessThanOrEqual(solo.height + 1)
    expect(screen.getByTestId('hidden-children')).toHaveAccessibleName('3 started sessions hidden')
    expect(screen.getByTestId('hidden-children')).toHaveTextContent('↳ 3')
    expect(within(cardEl as HTMLElement).getByTestId('status-dot')).toHaveAttribute('title', 'running')
    expect(screen.queryByTestId('kept-children')).toBeNull()
    expect(screen.queryByTestId('waiting-banner')).toBeNull()
    expect(screen.queryByText('Asking worker')).toBeNull()
    expect(screen.queryByText('Busy worker')).toBeNull()
    expect(screen.queryByText('Idle worker')).toBeNull()
    // Tile previews reuse the saved collapse choice on both a phone and a wide desktop.
    await userEvent.click(screen.getByRole('button', { name: 'Collapse session list' }))
    const tile = document.querySelector<HTMLButtonElement>('[data-session-id="parent"]')!
    expect(tile).toHaveAccessibleName(/running, 3 started sessions/)
    expect(tile).toHaveTextContent('↳3')
    await userEvent.hover(tile)
    const preview = await screen.findByTestId('hovercard')
    expect(within(preview).getByRole('button', { name: 'Show 3 started sessions' })).toHaveAttribute('aria-expanded', 'false')
    for (const title of ['Idle worker', 'Asking worker', 'Busy worker']) expect(within(preview).queryByText(title)).toBeNull()
    act(() => useSessionStore.getState().updateSessionStatus('c', 'idle'))
    expect(tile).toHaveAccessibleName(/idle, 3 started sessions/)
    expect(within(tile).getByTestId('status-dot')).toHaveTextContent('Zz')
    expect(screen.queryByTestId('waiting-banner')).toBeNull()
    await userEvent.click(within(preview).getByRole('button', { name: 'Show 3 started sessions' }))
    expect(within(preview).getByText('Asking worker')).toBeVisible()
    const waiting = within(preview).getByRole('img', { name: 'Waiting for its parent session' })
    expect(waiting).toHaveAttribute('data-status', 'waiting-parent')
    expect(getComputedStyle(waiting).color).toBe(getComputedStyle(within(preview).getAllByTestId('status-dot').find(g => g.textContent === 'Zz')!).color)
    expect(within(preview).queryByText('?')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Toggle session list' }))
    const branch = document.querySelector<HTMLElement>('[data-session-branch="parent"]')!
    expect(within(branch).getByText('Asking worker')).toBeVisible()
    await userEvent.click(within(branch).getByRole('button', { name: 'Hide 3 started sessions' }))
    expect(within(branch).queryByText('Asking worker')).toBeNull()
  })
})

it.each([390, 3840])('keeps runtime icons first and fixed when task chips change at %ipx', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  localStorage.clear()
  const sessions = [
    makeSession({ id: 'parent', title: 'Parent work', runtime: 'daemon', kind: 'agent', repo: 'widget', model: 'model-next', status: 'running' }),
    makeSession({ id: 'child', title: 'Child work', runtime: 'daemon', kind: 'agent', repo: 'widget', model: 'model-next', status: 'running', parent_session_id: 'parent' }),
    makeSession({ id: 'other', title: 'Other repo work', runtime: 'daemon', kind: 'agent', repo: 'other-widget', model: 'model-next', status: 'running', parent_session_id: 'parent' }),
  ]
  useSessionStore.setState({ sessions, daemons: [makeDaemon()] })
  render(<MemoryRouter><div style={{ width: width === 390 ? '100%' : 420 }}><SessionList variant="sidebar" /></div></MemoryRouter>)
  const cards = sessions.map(session => document.querySelector<HTMLElement>(`[data-session-branch="${session.id}"] > div`)!)
  const icons = cards.map(card => within(card).getByTestId('runtime-chip'))
  await document.fonts.ready
  const positions = icons.map(icon => icon.querySelector('.runtime-icon')!.getBoundingClientRect().x)
  for (const tasks of [0, 4, 0]) {
    act(() => useSessionStore.setState({ sessions: sessions.map(session => ({ ...session, background_tasks: tasks })) }))
    for (const [i, card] of cards.entries()) {
      const icon = within(card).getByTestId('runtime-chip')
      const row = icon.parentElement!
      expect.soft(row.firstElementChild).toBe(icon)
      expect.soft(icon.querySelector('.runtime-icon')!.getBoundingClientRect().x).toBeCloseTo(positions[i], 5)
      const items = [...row.children].map(item => item.getAttribute('data-testid'))
      expect.soft(items).toEqual(['runtime-chip', ...(i !== 1 ? ['session-repo'] : []), 'model-chip', ...(tasks ? ['background-chip'] : [])])
      expect(row.getBoundingClientRect().right).toBeLessThanOrEqual(width)
    }
  }
})

it.each([390, 3840])('promotes an orphan question to the waiting banner and tile at %ipx', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  const parent = makeSession({ id: 'parent', title: 'Parent', status: 'idle' })
  const child = makeSession({ id: 'child', title: 'Child question', parent_session_id: 'parent', status: 'waiting', unread: true })
  useSessionStore.setState({ sessions: [parent, child], daemons: [makeDaemon()] })
  render(<MemoryRouter><SessionList variant="sidebar" /></MemoryRouter>)
  expect(screen.getByRole('img', { name: 'Waiting for its parent session' })).toBeVisible()
  expect(screen.queryByTestId('waiting-banner')).toBeNull()
  act(() => useSessionStore.setState({ sessions: [{ ...parent, archived_at: '2026-10-19T12:00:00Z' }, child] }))
  expect(screen.getByRole('img', { name: 'waiting' })).toHaveTextContent('?')
  expect(screen.getByTestId('waiting-banner')).toHaveTextContent('1 session waiting for input')
  await userEvent.click(screen.getByRole('button', { name: 'Collapse session list' }))
  const tile = document.querySelector<HTMLButtonElement>('[data-session-id="child"]')!
  expect(tile).toHaveAccessibleName('Child question, waiting, unread')
  expect(within(tile).getByTestId('status-dot')).toHaveTextContent('?')
})
