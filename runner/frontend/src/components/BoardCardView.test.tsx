import { afterEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { PinnedTicketModal } from './BoardCardView'
import { OPEN_SESSION_BOARD } from '../lib/openSessionBoard'
import { resetSessionBoardLinksForTest } from '../lib/sessionBoardLinks'
import { resetLiveBoardCardsForTest } from '../lib/liveBoardCard'

const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
afterEach(() => { apiFetch.mockReset(); resetLiveBoardCardsForTest(); resetSessionBoardLinksForTest() })

it('keeps the key and every compact header action named and usable', async () => {
  const close = vi.fn()
  const openTab = vi.fn()
  const host = vi.fn((event: Event) => event.preventDefault())
  window.addEventListener(OPEN_SESSION_BOARD, host)
  try {
    apiFetch.mockImplementation(async url => ({ ok: true, status: 200, json: async () => String(url).endsWith('/permissions')
      ? { can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [{ board_id: 'board-a', board_name: 'Board', level: 'read' }] }
      : { card: { id: 'card-145', board_id: 'board-a', number: 145, reference: 'BLG-145', version: 0, title: 'Readable phone headers', column_name: 'ready' }, url: 'https://board.example.test/boards/board-a/cards/145' } }))
    render(<MemoryRouter><PinnedTicketModal ticket={{ source: 'blerg-board', board_id: 'board-a', key: '#145', title: 'Readable phone headers' }} sessionId="sess-a" onClose={close} onOpenTab={openTab} /></MemoryRouter>)
    const key = await screen.findByRole('link', { name: 'BLG-145' })
    expect(key).toHaveAttribute('title', 'BLG-145')
    expect(screen.getByRole('link', { name: 'Open in Board' })).toHaveAttribute('title', 'Open in Board')
    const board = await screen.findByRole('button', { name: 'Open board tab' })
    expect(board).toHaveAttribute('title', 'Open board tab')
    fireEvent.click(screen.getByRole('button', { name: 'Open as tab' }))
    expect(openTab).toHaveBeenCalledOnce()
    fireEvent.click(board)
    expect(host.mock.calls.some(([event]) => (event as CustomEvent).detail.boardId === 'board-a')).toBe(true)
    expect(close).toHaveBeenCalledOnce()
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    expect(close).toHaveBeenCalledTimes(2)
  } finally { window.removeEventListener(OPEN_SESSION_BOARD, host) }
})
