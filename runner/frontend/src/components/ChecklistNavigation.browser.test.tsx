// Real layout proof: a reference must scroll the intended item inside a long Plan list.
import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import '../index.css'
import { seedRichRail } from '../test/railFixture'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'
import SessionDetail from './SessionDetail'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async orig => ({ ...await orig<typeof import('../authClient')>(), getAccessToken: () => 'test-token', redirectToRefresh: vi.fn() }))
vi.mock('../claims', async orig => ({ ...await orig<typeof import('../claims')>(), tokenExpired: () => false }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } }))

beforeEach(async () => {
  await page.viewport(1280, 600)
  localStorage.removeItem(RAIL_PREF_KEY)
  localStorage.removeItem(RAIL_TAB_KEY)
  const { live } = seedRichRail()
  const worker = live.subagents[0].key
  const checklist = { event_id: 'checklist-snapshot', updated_at: new Date().toISOString(), items: Array.from({ length: 50 }, (_, i) => ({
    id: `item-${i}`, text: `Step ${i + 1}: verify the behavior`, done: false,
    ...(i === 44 ? { in_progress: true, assignees: [{ kind: 'subagent', id: worker }] } : {}),
  })) }
  apiFetch.mockImplementation((url: string) => json(url.endsWith('/live') ? { ...live, checklist } : url.startsWith('/api/tool-changes') ? { tool_changes: [] } : {}))
  vi.stubGlobal('fetch', vi.fn(() => json({ artifacts: [] })))
})
afterEach(() => { vi.unstubAllGlobals(); localStorage.clear() })

it.each(['mouse', 'keyboard'])('opens Plan by %s, scrolls the exact assigned row into view, pulses twice, and clears the cue', async input => {
  render(<div style={{ height: '100vh' }}><MemoryRouter initialEntries={['/sessions/sess-a']}>
    <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
  </MemoryRouter></div>)
  const rail = screen.getByTestId('session-rail')
  const reference = await within(rail).findByRole('button', { name: 'Plan · Step 45: verify the behavior' })
  if (input === 'keyboard') { reference.focus(); await userEvent.keyboard('{Enter}') }
  else await userEvent.click(reference)
  await waitFor(() => expect(document.activeElement).toHaveAttribute('data-checklist-id', 'item-44'))
  const row = document.activeElement as HTMLElement
  const body = rail.querySelector('.rail-body') as HTMLElement
  await waitFor(() => {
    const r = row.getBoundingClientRect(), b = body.getBoundingClientRect()
    expect(r.top).toBeGreaterThanOrEqual(b.top)
    expect(r.bottom).toBeLessThanOrEqual(b.bottom)
    expect(body.scrollTop).toBeGreaterThan(0)
  })
  expect(row).toHaveClass('checklist-highlight')
  expect(getComputedStyle(row).outlineStyle).toBe('none')
  expect(getComputedStyle(row).animationName).toBe('checklist-locate')
  expect(getComputedStyle(row).animationIterationCount).toBe('2')
  expect(getComputedStyle(row).animationDuration).toBe('0.7s')
  expect(getComputedStyle(row.querySelector('.checklist-box')!).boxShadow).toBe('none')
  const pulse = row.getAnimations().find(animation => animation instanceof CSSAnimation && animation.animationName === 'checklist-locate')!
  pulse.pause()
  pulse.currentTime = 0
  const neutral = getComputedStyle(row).backgroundColor
  pulse.currentTime = 350
  const peak = getComputedStyle(row).backgroundColor
  expect(peak).not.toBe(neutral)
  pulse.currentTime = 700
  expect(getComputedStyle(row).backgroundColor).toBe(neutral)
  pulse.currentTime = 1050
  expect(getComputedStyle(row).backgroundColor).toBe(peak)
  pulse.currentTime = 1400
  expect(getComputedStyle(row).backgroundColor).toBe('rgba(0, 0, 0, 0)')
  await waitFor(() => expect(row).not.toHaveClass('checklist-highlight'), { timeout: 5000 })
  expect(getComputedStyle(row).outlineStyle).toBe('none')
  expect(getComputedStyle(row).backgroundColor).toBe('rgba(0, 0, 0, 0)')
})

it.each([1280, 390])('keeps an assigned item on one line, its glyph beside the title, with subtle dividers at %ipx', async width => {
  await page.viewport(width, 700)
  render(<div style={{ height: '100vh' }}><MemoryRouter initialEntries={['/sessions/sess-a']}>
    <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
  </MemoryRouter></div>)
  if (!screen.queryByTestId('session-rail')) await userEvent.click(screen.getByTestId('rail-toggle'))
  const rail = await screen.findByTestId('session-rail')
  await userEvent.click(await within(rail).findByRole('tab', { name: /Plan/ }))
  const row = rail.querySelector('[data-checklist-id="item-44"]') as HTMLElement
  const text = row.querySelector('.checklist-text') as HTMLElement
  const assigned = row.querySelector('.rail-plan-assignees') as HTMLElement
  await waitFor(() => {
    const r = row.getBoundingClientRect(), t = text.getBoundingClientRect(), a = assigned.getBoundingClientRect()
    expect(t.width).toBeGreaterThan(r.width - 80)
    expect(a.left).toBeGreaterThanOrEqual(t.right - 1)
    expect(a.top).toBeGreaterThanOrEqual(r.top - 1)
    expect(a.right).toBeLessThanOrEqual(r.right + 1)
    expect(r.height).toBeLessThan(32)
  })
  expect(getComputedStyle(text).whiteSpace).toBe('nowrap')
  expect(getComputedStyle(text).textOverflow).toBe('ellipsis')
  expect(getComputedStyle(row).borderTopWidth).toBe('1px')
  expect(getComputedStyle(rail.querySelector('.checklist-items li')!).borderTopWidth).toBe('0px')
})
