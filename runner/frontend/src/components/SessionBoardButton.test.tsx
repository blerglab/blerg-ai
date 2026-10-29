import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import SessionBoardButton from './SessionBoardButton'
import { boardLinksOf, publishSessionPermissions, resetSessionBoardLinksForTest } from '../lib/sessionBoardLinks'
import { readPermissions, type BoardAccess } from '../lib/sessionPermissions'

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../lib/boardLink', async original => ({ ...await original<typeof import('../lib/boardLink')>(), useBoardOrigin: () => 'https://board.example.com' }))

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const view = (boards: BoardAccess[]) => ({ can_grant: true, reason: '', grants: [], mcp_tools: [], boards })
function serve(boards: BoardAccess[], names: { id: string; name: string }[] = []) {
  apiFetch.mockImplementation(async (url: string) => url.endsWith('/permissions') ? json(view(boards)) : url === '/api/session-boards' ? json({ configured: true, boards: names }) : json({}, 404))
}

beforeEach(() => { vi.useRealTimers(); resetSessionBoardLinksForTest(); apiFetch.mockReset() })
afterEach(() => { vi.useRealTimers() })

describe('session board links', () => {
  it('lists the launch board first, skips propose, and keeps one entry per board', () => {
    const links = boardLinksOf(readPermissions(view([
      { board_id: 'b-ops', board_name: 'Ops', level: 'read' },
      { board_id: 'b-x', board_name: 'Proposals only', level: 'propose' },
      { board_id: 'b-zeta', board_name: 'Zeta', level: 'full', launch: true },
      { board_id: 'b-ops', board_name: 'Ops', level: 'file' },
      { board_id: 'b-alpha', board_name: 'Alpha', level: 'file' },
    ])))
    expect(links).toEqual([
      { id: 'b-zeta', name: 'Zeta', launch: true },
      { id: 'b-alpha', name: 'Alpha', launch: false },
      { id: 'b-ops', name: 'Ops', launch: false },
    ])
  })
})

describe('board button', () => {
  it('renders nothing for a session without a board', async () => {
    serve([{ board_id: 'b-x', board_name: 'Proposals only', level: 'propose' }])
    const { container } = render(<SessionBoardButton sessionId="s1" />)
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/sessions/s1/permissions', expect.objectContaining({ signal: expect.any(AbortSignal) })))
    await act(async () => { await Promise.resolve() })
    expect(container.innerHTML).toBe('')
  })

  it('renders nothing when the permissions cannot be read', async () => {
    apiFetch.mockResolvedValue(json({}, 404))
    const { container } = render(<SessionBoardButton sessionId="s1" />)
    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
    await act(async () => { await Promise.resolve() })
    expect(container.innerHTML).toBe('')
  })

  it('links one board in a new tab, named from discovery when the launch board has no name', async () => {
    serve([{ board_id: 'b 1', level: 'full', launch: true }], [{ id: 'b 1', name: 'Release' }])
    render(<SessionBoardButton sessionId="s1" />)
    const link = await screen.findByRole('link', { name: 'Open board: Release' })
    expect(link.getAttribute('href')).toBe('https://board.example.com/boards/b%201')
    expect(link.getAttribute('target')).toBe('_blank')
    expect(link.getAttribute('rel')).toBe('noopener')
    expect(link.className).toContain('icon-btn')
  })

  it('opens a menu of several boards, launch board first, each in a new tab', async () => {
    serve([{ board_id: 'b-ops', board_name: 'Ops', level: 'read' }, { board_id: 'b-rel', board_name: 'Release', level: 'full', launch: true }])
    render(<SessionBoardButton sessionId="s1" />)
    const button = await screen.findByRole('button', { name: 'Open a board' })
    expect(button.getAttribute('aria-expanded')).toBe('false')
    fireEvent.click(button)
    const items = await screen.findAllByRole('menuitem')
    expect(items.map(i => i.textContent)).toEqual(['Releaselaunch board', 'Ops'])
    expect(items.map(i => i.getAttribute('href'))).toEqual(['https://board.example.com/boards/b-rel', 'https://board.example.com/boards/b-ops'])
    expect(items.every(i => i.getAttribute('target') === '_blank' && i.getAttribute('rel') === 'noopener')).toBe(true)
    expect(document.querySelector('.session-board-menu.confirm-popover')).toBeTruthy()
    fireEvent.keyDown(items[0], { key: 'Escape' })
    expect(screen.queryByRole('menu')).toBeNull()
    expect(document.activeElement).toBe(button)
  })

  it('follows the panel: a board given there shows without another request', async () => {
    serve([])
    const { container } = render(<SessionBoardButton sessionId="s1" />)
    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
    await act(async () => { await Promise.resolve() })
    expect(container.innerHTML).toBe('')
    await act(async () => { publishSessionPermissions('s1', readPermissions(view([{ board_id: 'b-ops', board_name: 'Ops', level: 'file' }]))) })
    expect(await screen.findByRole('link', { name: 'Open board: Ops' })).toBeTruthy()
    expect(apiFetch).toHaveBeenCalledTimes(1)
  })
})
