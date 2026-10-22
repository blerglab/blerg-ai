// Real layout for the Plan tab's checklist (card #111): one line per item at a phone's width and at
// 4K, the title cut with an ellipsis and shown whole on a click, the glyphs and the one commit chip on
// the same line, nothing past the panel's edge.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import '../index.css'
import SessionRail from './SessionRail'
import { makeSession } from '../test/fixtures'
import { RAIL_TAB_KEY } from '../lib/sessionRail'
import { CHILD_ID, SHARED_DIFF, SUB_ID, clutteredItems, clutteredTitles, makeLive } from '../test/planTabFixture'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async orig => ({ ...await orig<typeof import('../authClient')>(), getAccessToken: () => 'test-token', redirectToRefresh: vi.fn() }))
vi.mock('../claims', async orig => ({ ...await orig<typeof import('../claims')>(), tokenExpired: () => false }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } }))

beforeEach(() => {
  localStorage.clear()
  apiFetch.mockImplementation(() => json({}))
  vi.stubGlobal('fetch', vi.fn(() => json({ artifacts: [] })))
})
afterEach(() => vi.unstubAllGlobals())

it.each([390, 3840])('draws one line per item, folds the done ones, and opens the diff from the chip at %ipx', async width => {
  await page.viewport(width, 900)
  localStorage.setItem(RAIL_TAB_KEY, 'plan')
  const open = vi.fn()
  render(<div style={{ height: '100vh', display: 'flex', justifyContent: 'flex-end' }}><MemoryRouter>
    <SessionRail id="rail" session={makeSession({ status: 'running' })} toolChanges={0} mode="panel" open onClose={() => {}} onOpenArtifact={open}
      live={makeLive({ checklist: { items: clutteredItems, eventId: 'c1', updatedAt: null, workerTitles: clutteredTitles } as never })} />
  </MemoryRouter></div>)
  const list = await screen.findByTestId('rail-plan-list')
  const visible = () => within(list).getAllByTestId('rail-plan-row').filter(r => !r.hidden)
  expect(visible()).toHaveLength(3)
  await userEvent.click(within(list).getByTestId('rail-plan-done-toggle'))
  expect(visible()).toHaveLength(clutteredItems.length)
  const box = list.getBoundingClientRect()
  for (const row of visible()) {
    const r = row.getBoundingClientRect()
    const title = row.querySelector('.rail-plan-title') as HTMLElement
    // One line: no taller than a chip's line, and every part of the row on it.
    expect(r.height, row.dataset.checklistId).toBeLessThan(32)
    expect(getComputedStyle(title).textOverflow).toBe('ellipsis')
    for (const part of row.children) {
      const p = part.getBoundingClientRect()
      if (p.height === 0) continue
      expect(p.top).toBeGreaterThanOrEqual(r.top - 1)
      expect(p.bottom).toBeLessThanOrEqual(r.bottom + 1)
      expect(p.right).toBeLessThanOrEqual(box.right + 1)
    }
  }
  // The long done titles are cut at the phone's width; the chip shows once and sits on the right.
  const first = visible().find(r => r.dataset.checklistId === 'done-0')!
  const title = first.querySelector('.rail-plan-title') as HTMLElement
  if (width === 390) expect(title.scrollWidth).toBeGreaterThan(title.clientWidth)
  const chips = within(list).getAllByTestId('rail-plan-commit')
  expect(chips).toHaveLength(1)
  expect(chips[0].getBoundingClientRect().right).toBeGreaterThan(first.getBoundingClientRect().right - 12)
  // A click shows the whole title.
  await userEvent.click(title)
  expect(title).toHaveAttribute('data-expanded', 'true')
  expect(within(first).getByTestId('rail-plan-expand')).toHaveAttribute('aria-expanded', 'true')
  if (width === 390) expect(first.getBoundingClientRect().height).toBeGreaterThan(32)
  // No id anywhere a person reads or hears.
  const names = [...list.querySelectorAll('*')].map(el => `${el.getAttribute('aria-label') ?? ''} ${el.getAttribute('title') ?? ''}`).join(' ')
  for (const id of [SUB_ID, CHILD_ID, 'toolu_']) expect(`${list.innerText} ${names}`).not.toContain(id)
  // The chip by keyboard opens the diff.
  chips[0].focus()
  await userEvent.keyboard('{Enter}')
  expect(open).toHaveBeenCalledWith(SHARED_DIFF, undefined)
  // Nothing in the list runs past its own width.
  expect(list.scrollWidth).toBeLessThanOrEqual(list.clientWidth)
})
