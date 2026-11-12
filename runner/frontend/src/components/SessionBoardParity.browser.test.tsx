import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { page } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import SessionBoardTabs from './SessionBoardTabs'
import type { SessionTabs } from '../hooks/useSessionTabs'
import type { SessionLive } from '../hooks/useSessionLive'
import { parityCards, parityColumns } from '../../../../packages/board/parityFixture'
import '../index.css'
import { boardPageCacheKey, clearBoardPageCache, rememberBoardPage } from '../lib/boardPageCache'
import { resetLiveBoardCardsForTest } from '../lib/liveBoardCard'
import { resetSessionBoardLinksForTest } from '../lib/sessionBoardLinks'

// Keep the shared ticket dialog's message store offline in these board fixtures.
vi.mock('../ws', () => import('../test/wsMock'))
// Supply the auth lifecycle reached by the shared dialog's imports.
vi.mock('../authClient', () => ({
 redirectToRefresh: vi.fn(), consumeAccessTokenFromFragment: vi.fn(), startTokenRenewal: vi.fn(),
 renewSilently: vi.fn(async () => 'no-answer' as const), ensureFreshToken: vi.fn(async () => true),
 getAccessToken: () => ['fixture', btoa(JSON.stringify({ sub: 'parity-person' })), 'fixture'].join('.')
}))
let movedColumn = 'lane-0'
let permissionsReady: Promise<void> = Promise.resolve()
let moves = 0
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn(async (url: string, init?: RequestInit) => {
  if (url.endsWith('/permissions')) await permissionsReady
  if (url.includes('/move?')) { moves++; movedColumn = JSON.parse(String(init?.body)).column_id }
  return new Response(JSON.stringify(
  url.endsWith('/permissions') ? { can_grant: true, reason: '', boards: [{ board_id: 'parity', board_name: 'Release work', level: 'full' }], grants: [], mcp_tools: [] }
    : url.includes('/view?') ? { columns: parityColumns, cards: parityCards.map(c => c.number === 1 ? { ...c, column_id: movedColumn } : c), releases: [], next_cursor: '' }
    : /\/[12]\?/.test(url) ? { card: { ...parityCards[url.includes('/2?') ? 1 : 0], ...(url.includes('/1?') ? { column_id: movedColumn } : {}), columns: parityColumns }, url: '' } : {}
  ), { status: 200 })
}) }))
afterEach(cleanup)
for (const warm of [false, true]) for (const width of [390, 3840]) it(`renders the session board path at ${width}px (${warm ? 'cached, pending permissions' : 'cold'})`, async () => {
  localStorage.clear(); resetLiveBoardCardsForTest(); resetSessionBoardLinksForTest(); clearBoardPageCache(); movedColumn = 'lane-0'; moves = 0
  if (warm) rememberBoardPage(boardPageCacheKey('parity-person', 'parity-session', 'parity'), { columns: parityColumns, cards: parityCards, releases: [] }, '', '', Date.now())
  let releasePermissions = () => {}
  permissionsReady = warm ? new Promise<void>(resolve => { releasePermissions = resolve }) : Promise.resolve()
  await page.viewport(width, width === 390 ? 844 : 1200)
  const target = document.createElement('div'); target.style.height = '100dvh'; target.style.display = 'flex'; document.body.append(target)
  const tabs = { open: vi.fn(), setBoardLabel: vi.fn(), tabs: [{ id: 'board:parity', view: { kind: 'board', boardId: 'parity', label: 'Release work' } }], visible: ['board:parity'], targetFor: () => target } as unknown as SessionTabs
  try {
    render(<MemoryRouter><SessionBoardTabs tabs={tabs} sessionId="parity-session" live={{ live: { subagents: [], tickets: [] }, children: [] } as unknown as SessionLive} /></MemoryRouter>)
    await screen.findByText(parityCards[0].title)
    for (const el of document.querySelectorAll('.shared-board-column, .shared-board-card')) expect(el.getBoundingClientRect().width).toBeLessThanOrEqual(290)
    expect(getComputedStyle(screen.getByText(parityCards[0].title)).fontWeight).toBe('400')
    expect(parseFloat(getComputedStyle(screen.getByText(parityCards[0].title)).lineHeight)).toBeCloseTo(20.3)
    expect(screen.getByLabelText('Filter board release').getBoundingClientRect().width).toBeLessThan(250)
    const shots = import.meta.env.VITE_BOARD_SHOTS_DIR
    if (shots) await page.screenshot({ path: `${shots}/session-${width}.png` })
    fireEvent.click(screen.getByRole('button', { name: /DEMO-1/ }))
    await screen.findByTestId('board-card-live')
    const details = screen.getByRole('dialog', { name: parityCards[0].title })
    expect(within(details).getByRole('heading', { name: parityCards[0].title })).toBeTruthy()
    expect(within(details).getByRole('button', { name: 'Open as tab' })).toBeTruthy()
    if (width === 390) expect(details.getBoundingClientRect().width).toBeGreaterThan(350)
    else {
      expect(details.getBoundingClientRect().right).toBeLessThanOrEqual(width)
      fireEvent.click(within(details).getByRole('button', { name: 'Close' }))
      fireEvent.click(screen.getByRole('button', { name: /DEMO-2/ }))
      await screen.findByRole('heading', { name: parityCards[1].title })
      fireEvent.click(screen.getByRole('button', { name: 'Close' }))
      fireEvent.click(screen.getByRole('button', { name: /DEMO-1/ }))
      await screen.findByRole('heading', { name: parityCards[0].title })
    }
    if (shots) await page.screenshot({ path: `${shots}/session-details-${width}.png` })
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Filter' }))
    fireEvent.click(within(screen.getByRole('group', { name: 'Visible columns' })).getByRole('button', { name: 'Done column' }))
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(document.querySelectorAll('.shared-board-column')).toHaveLength(3)
    fireEvent.click(screen.getByRole('button', { name: /DEMO-1/ }))
    const done = await screen.findByRole('button', { name: 'Done' })
    // Cached content can render before this mount has checked the workspace grant.
    // Finding the button alone does not mean it can accept a move.
    if (warm) { expect(done).toBeDisabled(); expect(moves).toBe(0) }
    releasePermissions()
    await waitFor(() => expect(done).toBeEnabled())
    fireEvent.click(done)
    await screen.findByText('DEMO-1 moved to hidden column Done.')
    expect(moves).toBe(1)
    expect(movedColumn).toBe('lane-3')
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    fireEvent.click(screen.getByRole('button', { name: 'Show all columns' }))
    expect(document.querySelectorAll('.shared-board-column')).toHaveLength(4)
  } finally { releasePermissions(); cleanup(); target.remove() }
})
