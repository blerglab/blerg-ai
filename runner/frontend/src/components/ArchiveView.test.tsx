// TDD: ArchiveView component tests.
// Run: cd frontend && npm test -- src/components/ArchiveView.test.tsx

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter, Routes, Route, useLocation } from 'react-router-dom'
import type { Ticket } from '../types'

// Mock boardApi before importing component
vi.mock('../lib/boardApi', () => ({
  getBoardArchive: vi.fn(),
  newOpId: vi.fn(() => 'op-test-123'),
}))

vi.mock('../ws', () => import('../test/wsMock'))

import * as boardApi from '../lib/boardApi'
import ArchiveView from './ArchiveView'

// ── fixtures ───────────────────────────────────────────────────────────────────

function makeTicket(over: Partial<Ticket> = {}): Ticket {
  return {
    id: 't1',
    board_id: 'b1',
    column_id: null,
    title: 'Old ticket',
    body: null,
    priority: 'medium',
    size: null,
    rank: 'a',
    archived_at: '2026-01-01T00:00:00Z',
    session_id: null,
    version: 1,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    tags: [],
    repos: [],
    blocked: false,
    ...over,
  }
}

function LocationProbe() {
  const loc = useLocation()
  return <div data-testid="location">{loc.pathname}</div>
}

function renderArchiveView(boardId = 'b1') {
  return render(
    <MemoryRouter initialEntries={[`/boards/${boardId}/archive`]}>
      <Routes>
        <Route path="/boards/:id/archive" element={<ArchiveView />} />
      </Routes>
      <LocationProbe />
    </MemoryRouter>,
  )
}

// ── setup ──────────────────────────────────────────────────────────────────────

beforeEach(() => {
  vi.clearAllMocks()

  vi.mocked(boardApi.getBoardArchive).mockResolvedValue({
    tickets: [],
    next_cursor: '',
  })
})

// ── tests ──────────────────────────────────────────────────────────────────────

describe('ArchiveView', () => {
  describe('loading', () => {
    it('calls getBoardArchive with the board id on mount', async () => {
      renderArchiveView('b1')
      await waitFor(() =>
        expect(boardApi.getBoardArchive).toHaveBeenCalledWith('b1', expect.any(Object)),
      )
    })

    it('shows a back link in the header', async () => {
      renderArchiveView()
      expect(await screen.findByTestId('back-button')).toBeInTheDocument()
    })
  })

  describe('ticket list', () => {
    it('renders archived ticket titles', async () => {
      vi.mocked(boardApi.getBoardArchive).mockResolvedValue({
        tickets: [makeTicket({ id: 't1', title: 'Old ticket' })],
        next_cursor: '',
      })
      renderArchiveView()
      expect(await screen.findByText('Old ticket')).toBeInTheDocument()
    })

    it('renders multiple archived tickets', async () => {
      vi.mocked(boardApi.getBoardArchive).mockResolvedValue({
        tickets: [
          makeTicket({ id: 't1', title: 'First old ticket' }),
          makeTicket({ id: 't2', title: 'Second old ticket' }),
        ],
        next_cursor: '',
      })
      renderArchiveView()
      await waitFor(() => {
        expect(screen.getByText('First old ticket')).toBeInTheDocument()
        expect(screen.getByText('Second old ticket')).toBeInTheDocument()
      })
    })

    it('shows empty state when no archived tickets', async () => {
      renderArchiveView()
      expect(await screen.findByTestId('empty-archive')).toBeInTheDocument()
    })

    it('each ticket row links to the ticket detail', async () => {
      vi.mocked(boardApi.getBoardArchive).mockResolvedValue({
        tickets: [makeTicket({ id: 't42', board_id: 'b1', title: 'Archived one' })],
        next_cursor: '',
      })
      renderArchiveView('b1')
      const link = await screen.findByTestId('ticket-row-t42')
      fireEvent.click(link)
      await waitFor(() =>
        expect(screen.getByTestId('location')).toHaveTextContent('/boards/b1/ticket/t42'),
      )
    })
  })

  describe('pagination', () => {
    it('does not show load-more when next_cursor is empty', async () => {
      vi.mocked(boardApi.getBoardArchive).mockResolvedValue({
        tickets: [makeTicket()],
        next_cursor: '',
      })
      renderArchiveView()
      await screen.findByText('Old ticket')
      expect(screen.queryByTestId('load-more-archive')).not.toBeInTheDocument()
    })

    it('shows load-more button when next_cursor is non-empty', async () => {
      vi.mocked(boardApi.getBoardArchive).mockResolvedValue({
        tickets: [makeTicket()],
        next_cursor: 'cursor-xyz',
      })
      renderArchiveView()
      expect(await screen.findByTestId('load-more-archive')).toBeInTheDocument()
    })

    it('load-more calls getBoardArchive with the next_cursor', async () => {
      vi.mocked(boardApi.getBoardArchive)
        .mockResolvedValueOnce({
          tickets: [makeTicket({ id: 't1' })],
          next_cursor: 'cursor-xyz',
        })
        .mockResolvedValueOnce({
          tickets: [makeTicket({ id: 't2', title: 'More ticket' })],
          next_cursor: '',
        })

      renderArchiveView('b1')
      const loadMore = await screen.findByTestId('load-more-archive')
      fireEvent.click(loadMore)

      await waitFor(() =>
        expect(boardApi.getBoardArchive).toHaveBeenNthCalledWith(
          2,
          'b1',
          expect.objectContaining({ cursor: 'cursor-xyz' }),
        ),
      )
    })

    it('appends tickets on load-more', async () => {
      vi.mocked(boardApi.getBoardArchive)
        .mockResolvedValueOnce({
          tickets: [makeTicket({ id: 't1', title: 'First' })],
          next_cursor: 'cursor-xyz',
        })
        .mockResolvedValueOnce({
          tickets: [makeTicket({ id: 't2', title: 'Second' })],
          next_cursor: '',
        })

      renderArchiveView()
      const loadMore = await screen.findByTestId('load-more-archive')
      fireEvent.click(loadMore)

      await waitFor(() => {
        expect(screen.getByText('First')).toBeInTheDocument()
        expect(screen.getByText('Second')).toBeInTheDocument()
      })
    })
  })
})
