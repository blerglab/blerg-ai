// Real layout for the Running now cards (two lines each): at a phone's width and at 4K every card is
// the state glyph, title and clock, then the step and small glyphs; nothing names a raw id; a click on
// a card (and Enter on its title) brings its place in the conversation into view.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
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

const CHILD = '7f3c2a10-5b9e-4c1d-8e2f-0a1b2c3d4e5f'
let liveBody: unknown
beforeEach(() => {
  localStorage.removeItem(RAIL_TAB_KEY)
  localStorage.setItem(RAIL_PREF_KEY, '1')
  const { live } = seedRichRail()
  liveBody = { ...live, children: [{ id: CHILD, title: 'Port the parser', engine: 'codex', model: 'gpt-5.5-codex', effort: 'medium', status: 'running', started_at: new Date(Date.now() - 120_000).toISOString(), asking: false, tokens: 40_000, activity: 'Bash · go test ./...' }] }
  apiFetch.mockImplementation((url: string) => json(url.endsWith('/live') ? liveBody : url.startsWith('/api/tool-changes') ? { tool_changes: [] } : {}))
  vi.stubGlobal('fetch', vi.fn(() => json({ artifacts: [] })))
})
afterEach(() => { vi.unstubAllGlobals(); localStorage.clear() })

it.each([390, 3840])('draws every Running now card in two lines at %ipx, names no id, and a click jumps', async width => {
  await page.viewport(width, width === 3840 ? 2160 : 900)
  render(<div style={{ height: '100vh' }}><MemoryRouter initialEntries={['/sessions/sess-a']}>
    <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
  </MemoryRouter></div>)
  if (!screen.queryByTestId('session-rail')) await userEvent.click(screen.getByTestId('rail-toggle'))
  const rail = await screen.findByTestId('session-rail')
  const running = within(rail).getByTestId('rail-running')
  await waitFor(() => expect(within(running).getAllByTestId('rail-subagent')).toHaveLength(2))
  await waitFor(() => expect(within(running).getAllByTestId('rail-child')).toHaveLength(1))
  const cards = [...within(running).getAllByTestId('rail-subagent'), ...within(running).getAllByTestId('rail-task'), ...within(running).getAllByTestId('rail-child')]
  expect(cards.length).toBeGreaterThanOrEqual(7)
  for (const card of cards) {
    const lines = [...card.querySelectorAll(':scope > .rail-run-line')]
    expect(lines.length, card.textContent ?? '').toBeGreaterThanOrEqual(1)
    expect(lines.length).toBeLessThanOrEqual(2)
    for (const line of lines) expect(line.getBoundingClientRect().height, card.textContent ?? "").toBeLessThan(25)
    expect(card.getBoundingClientRect().height).toBeLessThan(60)
    expect(card.scrollWidth).toBeLessThanOrEqual(card.clientWidth)
    // No separate "Show in conversation" link and none of the old pills.
    expect(card.textContent).not.toMatch(/Show in conversation|in the background|effort:|general-purpose/)
  }
  // No raw id in what a person reads or hears.
  const names = [...running.querySelectorAll('*')].map(el => `${el.getAttribute('aria-label') ?? ''} ${el.getAttribute('title') ?? ''} ${el.getAttribute('aria-description') ?? ''}`).join(' ')
  expect(`${running.innerText} ${names}`).not.toContain(CHILD)
  // The words moved into the title's description (and the row's hovercard; card #129).
  const bg = within(running).getAllByTestId('rail-subagent').find(c => c.dataset.status === 'background')!
  expect(within(bg).getByTestId('rail-show').getAttribute('aria-description')).toMatch(/in the background · general-purpose · sonnet-5 · effort: /)
  // A click on the card's second line (a glyph) brings the subagent's card in the chat into view.
  const scrolled = vi.spyOn(Element.prototype, 'scrollIntoView')
  const sub = within(running).getAllByTestId('rail-subagent').find(c => c.dataset.key === 'A1')!
  await userEvent.click(sub.querySelector('.rail-run-glyphs [role="img"]')!)
  await waitFor(() => expect(scrolled, `click: ${document.querySelector('.rail-missing')?.textContent ?? 'no note'}`).toHaveBeenCalled())
  expect((scrolled.mock.contexts.at(-1) as HTMLElement).closest('[data-testid="subagent-card"]')).not.toBeNull()
  // And Enter on the title, from the keyboard (on a phone the jump closed the drawer: open it again).
  scrolled.mockClear()
  if (!screen.queryByTestId('session-rail')) await userEvent.click(screen.getByTestId('rail-toggle'))
  const again = within(screen.getByTestId('rail-running')).getAllByTestId('rail-subagent').find(c => c.dataset.key === 'A1')!
  within(again).getByTestId('rail-show').focus()
  await userEvent.keyboard('{Enter}')
  await waitFor(() => expect(scrolled, 'keyboard').toHaveBeenCalled())
  scrolled.mockRestore()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})
