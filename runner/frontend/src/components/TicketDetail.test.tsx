// TDD: TicketDetail component tests.
// Run: cd frontend && npm test -- src/components/TicketDetail.test.tsx

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import type { Ticket, Column } from '../types'
import type { TicketDetail as TicketDetailData } from '../types'
import { useBoardStore } from '../hooks/useBoardStore'

// Mock boardApi before importing component
vi.mock('../lib/boardApi', () => ({
  getTicket: vi.fn(),
  updateTicket: vi.fn(),
  moveTicket: vi.fn(),
  splitTicket: vi.fn(),
  archiveTicket: vi.fn(),
  addDependency: vi.fn(),
  removeDependency: vi.fn(),
  listTicketEvents: vi.fn(),
  newOpId: vi.fn(() => 'op-test-123'),
  ApiError: class ApiError extends Error {
    readonly status: number
    constructor(status: number, message: string) {
      super(message)
      this.name = 'ApiError'
      this.status = status
    }
  },
}))

vi.mock('../ws', () => import('../test/wsMock'))

import * as boardApi from '../lib/boardApi'
import TicketDetail from './TicketDetail'

// ── fixtures ───────────────────────────────────────────────────────────────────

function makeTicket(over: Partial<Ticket> = {}): Ticket {
  return {
    id: 't1',
    board_id: 'b1',
    column_id: 'col-todo',
    title: 'Fix the bug',
    body: 'Original body text',
    priority: 'medium',
    size: null,
    rank: 'a',
    archived_at: null,
    session_id: null,
    version: 3,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    tags: ['backend'],
    repos: ['org/repo'],
    blocked: false,
    ...over,
  }
}

function makeTicketDetail(over: Partial<TicketDetailData> = {}): TicketDetailData {
  return {
    ...makeTicket(),
    depends_on: [],
    blocks: [],
    ...over,
  }
}

function makeColumn(over: Partial<Column> = {}): Column {
  return {
    id: 'col-todo',
    board_id: 'b1',
    rank: 'a',
    name: 'Todo',
    is_terminal: false,
    created_at: '2026-01-01T00:00:00Z',
    ...over,
  }
}

function makeEvent(over: Partial<{ id: number; type: string; actor: string }> = {}) {
  return {
    id: 1,
    ticket_id: 't1',
    type: 'created',
    actor: 'human',
    from_column_id: null,
    to_column_id: null,
    data: null,
    created_at: '2026-01-01T00:00:00Z',
    ...over,
  }
}

function renderTicketDetail(boardId = 'b1', ticketId = 't1') {
  return render(
    <MemoryRouter initialEntries={[`/boards/${boardId}/ticket/${ticketId}`]}>
      <Routes>
        <Route path="/boards/:id/ticket/:ticketId" element={<TicketDetail />} />
      </Routes>
    </MemoryRouter>,
  )
}

// ── setup ──────────────────────────────────────────────────────────────────────

const COLUMNS = {
  'col-todo': makeColumn({ id: 'col-todo', name: 'Todo', rank: 'a' }),
  'col-done': makeColumn({ id: 'col-done', name: 'Done', rank: 'b', is_terminal: true }),
}

const TICKETS = {
  't2': makeTicket({ id: 't2', title: 'Blocker ticket', column_id: 'col-todo' }),
}

beforeEach(() => {
  vi.clearAllMocks()

  // Reset board store
  useBoardStore.setState({
    board: null,
    boardId: 'b1',
    columns: COLUMNS,
    tickets: TICKETS,
    loading: false,
    pendingOpIds: new Set(),
  })

  // Default mock responses
  vi.mocked(boardApi.getTicket).mockResolvedValue(makeTicketDetail())
  vi.mocked(boardApi.updateTicket).mockResolvedValue(makeTicket({ version: 4 }))
  vi.mocked(boardApi.moveTicket).mockResolvedValue(makeTicket())
  vi.mocked(boardApi.splitTicket).mockResolvedValue([makeTicket({ id: 'c1' }), makeTicket({ id: 'c2' })])
  vi.mocked(boardApi.archiveTicket).mockResolvedValue(makeTicket({ archived_at: '2026-01-01T00:00:00Z' }))
  vi.mocked(boardApi.addDependency).mockResolvedValue(undefined)
  vi.mocked(boardApi.removeDependency).mockResolvedValue(undefined)
  vi.mocked(boardApi.listTicketEvents).mockResolvedValue({
    events: [],
    next_cursor: '',
  })
})

// ── tests ──────────────────────────────────────────────────────────────────────

describe('TicketDetail', () => {
  describe('loading', () => {
    it('calls getTicket with the ticketId from the route', async () => {
      renderTicketDetail('b1', 't1')
      await waitFor(() => expect(boardApi.getTicket).toHaveBeenCalledWith('t1'))
    })

    it('shows the ticket title after loading', async () => {
      renderTicketDetail()
      expect(await screen.findByText('Fix the bug')).toBeInTheDocument()
    })
  })

  describe('body editing', () => {
    it('renders a textarea with the ticket body', async () => {
      renderTicketDetail()
      const textarea = await screen.findByTestId('body-textarea')
      expect(textarea).toHaveValue('Original body text')
    })

    it('calls updateTicket with body and expectVersion when saved', async () => {
      renderTicketDetail()
      const textarea = await screen.findByTestId('body-textarea')

      fireEvent.change(textarea, { target: { value: 'new body text' } })
      fireEvent.click(screen.getByTestId('save-body-button'))

      await waitFor(() =>
        expect(boardApi.updateTicket).toHaveBeenCalledWith(
          't1',
          expect.objectContaining({ body: 'new body text', expectVersion: 3 }),
        ),
      )
    })

    it('does not call updateTicket when body is unchanged', async () => {
      renderTicketDetail()
      await screen.findByTestId('body-textarea')

      fireEvent.click(screen.getByTestId('save-body-button'))

      await waitFor(() => expect(boardApi.updateTicket).not.toHaveBeenCalled())
    })

    it('refetches and shows the stale notice when the save returns a 409', async () => {
      // First getTicket load returns version 3; the save conflicts (409);
      // the refetch returns the authoritative version.
      vi.mocked(boardApi.getTicket)
        .mockResolvedValueOnce(makeTicketDetail({ version: 3 }))
        .mockResolvedValueOnce(makeTicketDetail({ version: 7, body: 'server body' }))
      vi.mocked(boardApi.updateTicket).mockRejectedValueOnce(
        new boardApi.ApiError(409, 'stale version'),
      )

      renderTicketDetail()
      const textarea = await screen.findByTestId('body-textarea')

      fireEvent.change(textarea, { target: { value: 'my edit' } })
      fireEvent.click(screen.getByTestId('save-body-button'))

      // (a) getTicket is called again to refetch the authoritative ticket
      await waitFor(() => expect(boardApi.getTicket).toHaveBeenCalledTimes(2))
      // (b) the stale notice appears
      expect(await screen.findByTestId('stale-notice')).toBeInTheDocument()
    })
  })

  describe('priority editor', () => {
    it('renders a priority select', async () => {
      renderTicketDetail()
      const sel = await screen.findByTestId('priority-select')
      expect(sel).toHaveValue('medium')
    })

    it('calls updateTicket with new priority when changed', async () => {
      renderTicketDetail()
      const sel = await screen.findByTestId('priority-select')
      fireEvent.change(sel, { target: { value: 'high' } })

      await waitFor(() =>
        expect(boardApi.updateTicket).toHaveBeenCalledWith(
          't1',
          expect.objectContaining({ priority: 'high' }),
        ),
      )
    })
  })

  describe('size editor', () => {
    it('renders a size select', async () => {
      renderTicketDetail()
      expect(await screen.findByTestId('size-select')).toBeInTheDocument()
    })

    it('calls updateTicket with new size when changed', async () => {
      renderTicketDetail()
      const sel = await screen.findByTestId('size-select')
      fireEvent.change(sel, { target: { value: 's' } })

      await waitFor(() =>
        expect(boardApi.updateTicket).toHaveBeenCalledWith(
          't1',
          expect.objectContaining({ size: 's' }),
        ),
      )
    })
  })

  describe('tags editor', () => {
    it('renders existing tags as chips', async () => {
      renderTicketDetail()
      expect(await screen.findByTestId('tag-chip-backend')).toBeInTheDocument()
    })

    it('calls updateTicket with addTags and expectVersion when a tag is added', async () => {
      renderTicketDetail()
      await screen.findByTestId('add-tag-input')

      fireEvent.change(screen.getByTestId('add-tag-input'), { target: { value: 'frontend' } })
      fireEvent.click(screen.getByTestId('add-tag-button'))

      await waitFor(() =>
        expect(boardApi.updateTicket).toHaveBeenCalledWith(
          't1',
          expect.objectContaining({ addTags: ['frontend'], expectVersion: 3 }),
        ),
      )
    })

    it('calls updateTicket with rmTags and expectVersion when a tag is removed', async () => {
      renderTicketDetail()
      const removeBtn = await screen.findByTestId('remove-tag-backend')
      fireEvent.click(removeBtn)

      await waitFor(() =>
        expect(boardApi.updateTicket).toHaveBeenCalledWith(
          't1',
          expect.objectContaining({ rmTags: ['backend'], expectVersion: 3 }),
        ),
      )
    })
  })

  describe('dependencies', () => {
    it('calls addDependency when a dep is added', async () => {
      renderTicketDetail()
      await screen.findByTestId('add-dep-input')

      fireEvent.change(screen.getByTestId('add-dep-input'), { target: { value: 't2' } })
      fireEvent.click(screen.getByTestId('add-dep-button'))

      await waitFor(() =>
        expect(boardApi.addDependency).toHaveBeenCalledWith(
          't1',
          expect.objectContaining({ dependsOnTicketId: 't2' }),
        ),
      )
    })

    it('shows depends_on tickets with unlink buttons', async () => {
      vi.mocked(boardApi.getTicket).mockResolvedValue(makeTicketDetail({ depends_on: ['t2'] }))
      renderTicketDetail()
      expect(await screen.findByTestId('remove-dep-t2')).toBeInTheDocument()
    })

    it('calls removeDependency when an unlink button is clicked', async () => {
      vi.mocked(boardApi.getTicket).mockResolvedValue(makeTicketDetail({ depends_on: ['t2'] }))
      renderTicketDetail()
      const removeBtn = await screen.findByTestId('remove-dep-t2')
      fireEvent.click(removeBtn)

      await waitFor(() =>
        expect(boardApi.removeDependency).toHaveBeenCalledWith('t1', 't2'),
      )
    })
  })

  describe('split', () => {
    it('renders split title inputs', async () => {
      renderTicketDetail()
      expect(await screen.findByTestId('split-title-0')).toBeInTheDocument()
      expect(screen.getByTestId('split-title-1')).toBeInTheDocument()
    })

    it('split button is disabled when fewer than 2 non-empty titles are entered', async () => {
      renderTicketDetail()
      expect(await screen.findByTestId('split-button')).toBeDisabled()
    })

    it('split button is enabled when 2+ non-empty titles are entered', async () => {
      renderTicketDetail()
      await screen.findByTestId('split-title-0')

      fireEvent.change(screen.getByTestId('split-title-0'), { target: { value: 'Child A' } })
      fireEvent.change(screen.getByTestId('split-title-1'), { target: { value: 'Child B' } })

      expect(screen.getByTestId('split-button')).not.toBeDisabled()
    })

    it('calls splitTicket with the entered titles when submitted', async () => {
      renderTicketDetail()
      await screen.findByTestId('split-title-0')

      fireEvent.change(screen.getByTestId('split-title-0'), { target: { value: 'Child A' } })
      fireEvent.change(screen.getByTestId('split-title-1'), { target: { value: 'Child B' } })
      fireEvent.click(screen.getByTestId('split-button'))

      await waitFor(() =>
        expect(boardApi.splitTicket).toHaveBeenCalledWith(
          't1',
          expect.objectContaining({ titles: ['Child A', 'Child B'] }),
        ),
      )
    })
  })

  describe('archive', () => {
    it('renders the archive button', async () => {
      renderTicketDetail()
      expect(await screen.findByTestId('archive-button')).toBeInTheDocument()
    })

    it('calls archiveTicket when archive button is clicked', async () => {
      renderTicketDetail()
      const btn = await screen.findByTestId('archive-button')
      fireEvent.click(btn)

      await waitFor(() => expect(boardApi.archiveTicket).toHaveBeenCalledWith('t1'))
    })

    it('shows archived banner when ticket is already archived', async () => {
      vi.mocked(boardApi.getTicket).mockResolvedValue(
        makeTicketDetail({ archived_at: '2026-01-01T00:00:00Z' }),
      )
      renderTicketDetail()
      expect(await screen.findByTestId('archived-banner')).toBeInTheDocument()
    })

    it('does not show archived banner for non-archived tickets', async () => {
      renderTicketDetail()
      await screen.findByTestId('archive-button')
      expect(screen.queryByTestId('archived-banner')).not.toBeInTheDocument()
    })
  })

  describe('session link', () => {
    it('shows a link to the bound session when session_id is set', async () => {
      vi.mocked(boardApi.getTicket).mockResolvedValue(
        makeTicketDetail({ session_id: 'sess-abc' }),
      )
      renderTicketDetail()
      expect(await screen.findByTestId('session-link')).toBeInTheDocument()
    })

    it('does not show session link when session_id is null', async () => {
      renderTicketDetail()
      await screen.findByText('Fix the bug')
      expect(screen.queryByTestId('session-link')).not.toBeInTheDocument()
    })
  })

  describe('move to', () => {
    it('shows the move-to button', async () => {
      renderTicketDetail()
      expect(await screen.findByTestId('move-to-button')).toBeInTheDocument()
    })

    it('calls moveTicket when a column is selected', async () => {
      renderTicketDetail()
      const btn = await screen.findByTestId('move-to-button')
      fireEvent.click(btn)

      const colBtn = await screen.findByTestId('move-to-col-done')
      fireEvent.click(colBtn)

      await waitFor(() =>
        expect(boardApi.moveTicket).toHaveBeenCalledWith(
          't1',
          expect.objectContaining({ columnId: 'col-done' }),
        ),
      )
    })
  })

  describe('activity feed', () => {
    it('calls listTicketEvents on mount', async () => {
      renderTicketDetail()
      await waitFor(() => expect(boardApi.listTicketEvents).toHaveBeenCalledWith('t1', expect.any(Object)))
    })

    it('renders events in the feed', async () => {
      vi.mocked(boardApi.listTicketEvents).mockResolvedValue({
        events: [makeEvent({ id: 1, type: 'created', actor: 'human' })],
        next_cursor: '',
      })
      renderTicketDetail()
      expect(await screen.findByTestId('event-1')).toBeInTheDocument()
    })

    it('shows load-more button when next_cursor is non-empty', async () => {
      vi.mocked(boardApi.listTicketEvents).mockResolvedValue({
        events: [makeEvent({ id: 1 })],
        next_cursor: 'cursor-abc',
      })
      renderTicketDetail()
      expect(await screen.findByTestId('load-more-events')).toBeInTheDocument()
    })

    it('does not show load-more when next_cursor is empty', async () => {
      vi.mocked(boardApi.listTicketEvents).mockResolvedValue({
        events: [makeEvent({ id: 1 })],
        next_cursor: '',
      })
      renderTicketDetail()
      await screen.findByTestId('event-1')
      expect(screen.queryByTestId('load-more-events')).not.toBeInTheDocument()
    })

    it('load-more calls listTicketEvents with the next_cursor', async () => {
      vi.mocked(boardApi.listTicketEvents)
        .mockResolvedValueOnce({
          events: [makeEvent({ id: 1 })],
          next_cursor: 'cursor-abc',
        })
        .mockResolvedValueOnce({
          events: [makeEvent({ id: 2 })],
          next_cursor: '',
        })

      renderTicketDetail()
      const loadMore = await screen.findByTestId('load-more-events')
      fireEvent.click(loadMore)

      await waitFor(() =>
        expect(boardApi.listTicketEvents).toHaveBeenNthCalledWith(
          2,
          't1',
          expect.objectContaining({ cursor: 'cursor-abc' }),
        ),
      )
    })

    it('appends loaded events to the feed on load-more', async () => {
      vi.mocked(boardApi.listTicketEvents)
        .mockResolvedValueOnce({
          events: [makeEvent({ id: 1, type: 'created' })],
          next_cursor: 'cursor-abc',
        })
        .mockResolvedValueOnce({
          events: [makeEvent({ id: 2, type: 'updated' })],
          next_cursor: '',
        })

      renderTicketDetail()
      const loadMore = await screen.findByTestId('load-more-events')
      fireEvent.click(loadMore)

      await waitFor(() => {
        expect(screen.getByTestId('event-1')).toBeInTheDocument()
        expect(screen.getByTestId('event-2')).toBeInTheDocument()
      })
    })
  })
})
