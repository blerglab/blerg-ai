// TDD: BoardView component tests.
// Run: cd frontend && npm test -- src/components/BoardView.test.tsx

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter, Routes, Route, useLocation } from 'react-router-dom'
import type { Board, Column, Ticket } from '../types'
import { useBoardStore } from '../hooks/useBoardStore'
import * as wsMock from '../test/wsMock'

// Mock boardApi before importing component
vi.mock('../lib/boardApi', () => ({
  getBoard: vi.fn(),
  spawnAssist: vi.fn(),
  createTicket: vi.fn(),
  moveTicket: vi.fn(),
  patchColumn: vi.fn(),
  addColumn: vi.fn(),
  deleteColumn: vi.fn(),
  newOpId: vi.fn(() => 'op-test-123'),
  listBoards: vi.fn(),
  deleteBoard: vi.fn(),
}))

vi.mock('../ws', () => import('../test/wsMock'))

import * as boardApi from '../lib/boardApi'
import BoardView from './BoardView'

// ── fixtures ───────────────────────────────────────────────────────────────────

function makeBoard(over: Partial<Board> = {}): Board {
  return {
    id: 'b1',
    name: 'Sprint Board',
    description: 'Board for the sprint',
    repos: ['org/repo'],
    default_daemon_id: null,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
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

function makeTicket(over: Partial<Ticket> = {}): Ticket {
  return {
    id: 't1',
    board_id: 'b1',
    column_id: 'col-todo',
    title: 'First ticket',
    body: null,
    priority: 'medium',
    size: null,
    rank: 'a',
    archived_at: null,
    session_id: null,
    version: 1,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    tags: ['backend'],
    repos: [],
    blocked: false,
    ...over,
  }
}

const BOARD = makeBoard()
const COLUMNS = [
  makeColumn({ id: 'col-todo', rank: 'a', name: 'Todo' }),
  makeColumn({ id: 'col-done', rank: 'b', name: 'Done', is_terminal: true }),
]
const TICKETS = [
  makeTicket({ id: 't1', title: 'First ticket', column_id: 'col-todo', tags: ['backend'] }),
  makeTicket({ id: 't2', title: 'Second ticket', column_id: 'col-todo', tags: ['frontend'], priority: 'low' }),
  makeTicket({ id: 't3', title: 'Done ticket', column_id: 'col-done', tags: [] }),
]

function LocationProbe() {
  const loc = useLocation()
  return <div data-testid="location">{loc.pathname}</div>
}

function renderBoardView(boardId = 'b1') {
  return render(
    <MemoryRouter initialEntries={[`/boards/${boardId}`]}>
      <Routes>
        <Route path="/boards/:id" element={<BoardView />} />
      </Routes>
      {/* Always-rendered probe: tracks the current URL even after navigation away */}
      <LocationProbe />
    </MemoryRouter>,
  )
}

// ── setup ──────────────────────────────────────────────────────────────────────

beforeEach(() => {
  // Clear mock call history so tests don't accumulate counts across runs
  vi.clearAllMocks()
  wsMock.resetWsMock()
  // Reset the board store between tests
  useBoardStore.setState({
    board: null,
    boardId: null,
    columns: {},
    tickets: {},
    loading: false,
    pendingOpIds: new Set(),
  })

  vi.mocked(boardApi.getBoard).mockResolvedValue({
    ...BOARD,
    columns: COLUMNS,
    tickets: TICKETS,
  })
  vi.mocked(boardApi.spawnAssist).mockResolvedValue({ session_id: 'sess-assist-1' })
  vi.mocked(boardApi.createTicket).mockResolvedValue(
    makeTicket({ id: 't-new', title: 'New ticket' }),
  )
  vi.mocked(boardApi.moveTicket).mockResolvedValue(makeTicket())
})

// ── tests ──────────────────────────────────────────────────────────────────────

describe('BoardView', () => {
  describe('initial load', () => {
    it('calls getBoard with the route id on mount', async () => {
      renderBoardView('b1')
      await waitFor(() => expect(boardApi.getBoard).toHaveBeenCalledWith('b1'))
    })

    it('shows the board name in the header after load', async () => {
      renderBoardView('b1')
      expect(await screen.findByText('Sprint Board')).toBeInTheDocument()
    })

    it('renders column headers after load', async () => {
      renderBoardView('b1')
      await waitFor(() => {
        expect(screen.getByText('Todo')).toBeInTheDocument()
        expect(screen.getByText('Done')).toBeInTheDocument()
      })
    })

    it('renders ticket cards after load', async () => {
      renderBoardView('b1')
      expect(await screen.findByText('First ticket')).toBeInTheDocument()
      expect(screen.getByText('Second ticket')).toBeInTheDocument()
      expect(screen.getByText('Done ticket')).toBeInTheDocument()
    })

    it('sends a subscribe_board WS message after load', async () => {
      renderBoardView('b1')
      await waitFor(() => {
        expect(wsMock.send).toHaveBeenCalledWith(
          expect.objectContaining({ type: 'subscribe_board', board_id: 'b1' }),
        )
      })
    })
  })

  describe('filter bar', () => {
    it('shows all tickets when no filter is active', async () => {
      renderBoardView('b1')
      await waitFor(() => expect(screen.getByText('First ticket')).toBeInTheDocument())
      expect(screen.getByText('Second ticket')).toBeInTheDocument()
    })

    it('hides tickets that do not match the active tag filter', async () => {
      renderBoardView('b1')
      await waitFor(() => expect(screen.getByText('First ticket')).toBeInTheDocument())

      // Find and click the 'backend' tag filter chip
      const backendFilter = screen.getByTestId('filter-tag-backend')
      fireEvent.click(backendFilter)

      await waitFor(() => {
        // First ticket has 'backend' tag — should be visible
        expect(screen.getByText('First ticket')).toBeInTheDocument()
        // Second ticket has 'frontend' tag — should be hidden
        expect(screen.queryByText('Second ticket')).not.toBeInTheDocument()
      })
    })

    it('re-shows hidden tickets when the filter is cleared', async () => {
      renderBoardView('b1')
      await waitFor(() => expect(screen.getByText('First ticket')).toBeInTheDocument())

      const backendFilter = screen.getByTestId('filter-tag-backend')
      fireEvent.click(backendFilter) // enable
      await waitFor(() => expect(screen.queryByText('Second ticket')).not.toBeInTheDocument())

      fireEvent.click(backendFilter) // disable
      await waitFor(() => expect(screen.getByText('Second ticket')).toBeInTheDocument())
    })
  })

  describe('live updates', () => {
    it('adds a new ticket card when a ticket_created WS event arrives', async () => {
      renderBoardView('b1')
      await waitFor(() => expect(screen.getByText('First ticket')).toBeInTheDocument())

      const newTicket = makeTicket({ id: 't-live', title: 'Live ticket', column_id: 'col-todo' })
      wsMock.emit({ type: 'ticket_created', ticket: newTicket, op_id: '' })

      await waitFor(() => expect(screen.getByText('Live ticket')).toBeInTheDocument())
      // Verify reload was NOT called for live update
      expect(boardApi.getBoard).toHaveBeenCalledTimes(1)
    })
  })

  describe('Assist button', () => {
    it('calls spawnAssist with the board id when Assist is clicked', async () => {
      renderBoardView('b1')
      await waitFor(() => expect(screen.getByText('Sprint Board')).toBeInTheDocument())

      fireEvent.click(screen.getByTestId('assist-button'))

      await waitFor(() => expect(boardApi.spawnAssist).toHaveBeenCalledWith('b1', expect.any(Object)))
    })

    it('navigates to the session terminal after Assist completes', async () => {
      renderBoardView('b1')
      await waitFor(() => expect(screen.getByText('Sprint Board')).toBeInTheDocument())

      fireEvent.click(screen.getByTestId('assist-button'))

      await waitFor(() => {
        expect(screen.getByTestId('location')).toHaveTextContent('/sessions/sess-assist-1')
      })
    })
  })

  describe('add column', () => {
    it('renders an "Add column" control in the header area', async () => {
      renderBoardView('b1')
      await waitFor(() => expect(screen.getByText('Sprint Board')).toBeInTheDocument())
      expect(screen.getByTestId('add-column-button')).toBeInTheDocument()
    })
  })

  describe('per-column ticket creation', () => {
    it('shows a "+" button for each column', async () => {
      renderBoardView('b1')
      await waitFor(() => expect(screen.getByText('Todo')).toBeInTheDocument())
      // There should be at least one add-ticket button
      expect(screen.getByTestId('add-ticket-col-todo')).toBeInTheDocument()
    })

    it('shows a title input when the "+" button is clicked', async () => {
      renderBoardView('b1')
      await waitFor(() => expect(screen.getByText('Todo')).toBeInTheDocument())

      fireEvent.click(screen.getByTestId('add-ticket-col-todo'))

      expect(screen.getByTestId('new-ticket-title-col-todo')).toBeInTheDocument()
    })

    it('calls createTicket with the column id and title on submit', async () => {
      renderBoardView('b1')
      await waitFor(() => expect(screen.getByText('Todo')).toBeInTheDocument())

      fireEvent.click(screen.getByTestId('add-ticket-col-todo'))
      const input = screen.getByTestId('new-ticket-title-col-todo')
      fireEvent.change(input, { target: { value: 'My new ticket' } })
      fireEvent.keyDown(input, { key: 'Enter' })

      await waitFor(() =>
        expect(boardApi.createTicket).toHaveBeenCalledWith(
          'b1',
          expect.objectContaining({ title: 'My new ticket', columnId: 'col-todo' }),
        ),
      )
    })
  })
})
