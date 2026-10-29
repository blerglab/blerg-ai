import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { page, userEvent } from 'vitest/browser'
import { useTranscriptStore } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { invalidateChatTransport } from '../lib/chatTransport'
import { resetSessionBoardLinksForTest } from '../lib/sessionBoardLinks'
import { resetBoardOriginForTest } from '../lib/boardLink'
import type { BoardAccess } from '../lib/sessionPermissions'
import '../index.css'
import '@blerglab/chat/blerg.css'

vi.mock('../shellTransport', async original => {
  const actual = await original<typeof import('../shellTransport')>()
  const { FakeShell } = await import('../test/shellMock')
  return { ...actual, openShell: vi.fn(async () => new FakeShell('tab-shell')), restoreShells: vi.fn(async () => []) }
})
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
let boards: BoardAccess[] = []
beforeEach(() => {
  localStorage.clear()
  resetSessionBoardLinksForTest()
  resetBoardOriginForTest()
  document.documentElement.setAttribute('data-theme', 'dark')
  useTranscriptStore.setState({ sessions: {} })
  useTranscriptStore.getState().ingestReplayDone('sess-a', { lastSeq: 0, hasMore: false })
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ kind: 'agent', status: 'idle', title: 'Plan the release' })] })
  apiFetch.mockReset().mockImplementation(async (url: string) => {
    if (url.includes('shell-capability')) return json({ available: true })
    if (url.includes('/live')) return json({ can_release_prompt: true })
    if (url.endsWith('/permissions')) return json({ can_grant: true, reason: '', grants: [], mcp_tools: [], boards })
    if (url === '/api/site-board') return json({ board_url: 'https://board.example.com' })
    if (url === '/api/session-boards') return json({ configured: true, boards: [{ id: 'b-rel', name: 'Release planning' }] })
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn(async () => json([])))
})
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals(); document.documentElement.removeAttribute('data-theme') })

function mount() {
  return render(<MemoryRouter initialEntries={['/sessions/sess-a']}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></MemoryRouter>)
}
const bar = () => screen.getByRole('navigation', { name: /Workspace|Session navigation/ })
function inside(el: Element) {
  const r = el.getBoundingClientRect()
  expect(r.width).toBeGreaterThan(0)
  expect(r.left).toBeGreaterThanOrEqual(0)
  expect(r.right).toBeLessThanOrEqual(innerWidth + 1)
  expect(r.bottom).toBeLessThanOrEqual(innerHeight + 1)
}

for (const [width, height] of [[390, 844], [3840, 2160]]) {
  it(`shows no board button for a session without a board at ${width}`, async () => {
    await page.viewport(width, height)
    boards = [{ board_id: 'b-x', board_name: 'Proposals only', level: 'propose' }]
    mount()
    await waitFor(() => expect(apiFetch.mock.calls.some(([u]) => String(u).endsWith('/sess-a/permissions'))).toBe(true))
    await waitFor(() => expect(within(bar()).getByTestId('rail-toggle')).toBeTruthy())
    expect(within(bar()).queryByTestId('session-board-button')).toBeNull()
  })

  it(`puts one board's link between Shell and the panel toggle at ${width}`, async () => {
    await page.viewport(width, height)
    boards = [{ board_id: 'b-rel', level: 'full', launch: true }]
    mount()
    const link = await within(bar()).findByRole('link', { name: 'Open board: Release planning' })
    await waitFor(() => expect(link.getAttribute('href')).toBe('https://board.example.com/boards/b-rel'))
    expect(link.getAttribute('target')).toBe('_blank')
    expect(link.getAttribute('rel')).toBe('noopener')
    const shell = await within(bar()).findByRole('button', { name: 'New shell' })
    const panel = within(bar()).getByTestId('rail-toggle')
    expect(link.previousElementSibling).toBe(shell)
    expect(link.nextElementSibling).toBe(panel)
    // The same size as its neighbours.
    expect(link.getBoundingClientRect().height).toBe(panel.getBoundingClientRect().height)
    inside(link)
  })

  it(`opens a popover of several boards at ${width}`, async () => {
    await page.viewport(width, height)
    boards = [{ board_id: 'b-ops', board_name: 'Ops', level: 'read' }, { board_id: 'b-rel', level: 'full', launch: true }]
    mount()
    const button = await within(bar()).findByRole('button', { name: 'Open a board' })
    await waitFor(() => expect(apiFetch.mock.calls.some(([u]) => u === '/api/site-board')).toBe(true))
    await new Promise(r => setTimeout(r, 0))
    expect(button.nextElementSibling).toBe(within(bar()).getByTestId('rail-toggle'))
    await userEvent.click(button)
    const menu = await screen.findByRole('menu', { name: 'Open a board' })
    const items = within(menu).getAllByRole('menuitem')
    expect(items.map(i => i.getAttribute('href'))).toEqual(['https://board.example.com/boards/b-rel', 'https://board.example.com/boards/b-ops'])
    expect(items[0].textContent).toContain('Release planning')
    await waitFor(() => expect(document.activeElement).toBe(items[0]))
    const card = screen.getByTestId('session-board-menu')
    await waitFor(() => expect(card.getAnimations().length).toBe(0))
    inside(card)
    for (const item of items) inside(item)
    if (width <= 640) expect(Math.round(card.getBoundingClientRect().width)).toBe(innerWidth)
    else expect(card.getBoundingClientRect().top).toBeGreaterThanOrEqual(button.getBoundingClientRect().bottom)
    await userEvent.keyboard('{ArrowDown}')
    expect(document.activeElement).toBe(items[1])
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('menu')).toBeNull())
    expect(document.activeElement).toBe(button)
  })
}

it('keeps the button for an archived session', async () => {
  await page.viewport(1440, 900)
  useSessionStore.setState({ sessions: [makeSession({ kind: 'agent', status: 'stopped', archived_at: '2026-10-26T00:00:00Z', title: 'Plan the release' })] })
  boards = [{ board_id: 'b-ops', board_name: 'Ops', level: 'read' }]
  mount()
  expect(await within(bar()).findByRole('link', { name: 'Open board: Ops' })).toBeTruthy()
})
