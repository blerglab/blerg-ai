// TDD: TicketCard component tests.
// Run: cd frontend && npm test -- src/components/TicketCard.test.tsx

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter, useLocation } from 'react-router-dom'
import type { Ticket, Column, SessionInfo } from '../types'

// Mock boardApi before importing the component
vi.mock('../lib/boardApi', () => ({
  moveTicket: vi.fn(),
  newOpId: vi.fn(() => 'op-test-123'),
  createTicket: vi.fn(),
  getBoard: vi.fn(),
  spawnAssist: vi.fn(),
}))

vi.mock('../ws', () => import('../test/wsMock'))

import * as boardApi from '../lib/boardApi'
import TicketCard from './TicketCard'

// ── fixtures ───────────────────────────────────────────────────────────────────

function makeTicket(over: Partial<Ticket> = {}): Ticket {
  return {
    id: 't1',
    board_id: 'b1',
    column_id: 'col-todo',
    title: 'Fix the bug',
    body: null,
    priority: 'high',
    size: 'm',
    rank: 'a',
    archived_at: null,
    session_id: null,
    version: 1,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    tags: ['backend', 'auth'],
    repos: ['org/repo'],
    blocked: false,
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

function makeSession(id: string, status: SessionInfo['status']): SessionInfo {
  return {
    id,
    daemon_id: 'd1',
    status,
    project_path: '/repos/foo',
    repo: 'foo',
    title: 'A session',
    started_at: '2026-01-01T00:00:00Z',
  }
}

const COLUMNS: Column[] = [
  makeColumn({ id: 'col-todo', name: 'Todo' }),
  makeColumn({ id: 'col-in-progress', rank: 'b', name: 'In Progress' }),
  makeColumn({ id: 'col-done', rank: 'c', name: 'Done' }),
]

function LocationProbe() {
  const loc = useLocation()
  return <div data-testid="location">{loc.pathname}</div>
}

function renderCard(ticket: Ticket, sessions: SessionInfo[] = []) {
  return render(
    <MemoryRouter initialEntries={['/']}>
      <TicketCard ticket={ticket} columns={COLUMNS} sessions={sessions} />
      <LocationProbe />
    </MemoryRouter>,
  )
}

// ── tests ──────────────────────────────────────────────────────────────────────

beforeEach(() => {
  vi.mocked(boardApi.moveTicket).mockResolvedValue(makeTicket())
  vi.mocked(boardApi.newOpId).mockReturnValue('op-test-123')
})

describe('TicketCard', () => {
  describe('rendering', () => {
    it('renders the ticket title', () => {
      renderCard(makeTicket({ title: 'Fix the bug' }))
      expect(screen.getByText('Fix the bug')).toBeInTheDocument()
    })

    it('renders the priority indicator with the correct data-priority', () => {
      renderCard(makeTicket({ priority: 'high' }))
      const indicator = screen.getByTestId('priority-indicator')
      expect(indicator).toBeInTheDocument()
      expect(indicator).toHaveAttribute('data-priority', 'high')
    })

    it('priority indicator reflects urgent priority', () => {
      renderCard(makeTicket({ priority: 'urgent' }))
      expect(screen.getByTestId('priority-indicator')).toHaveAttribute('data-priority', 'urgent')
    })

    it('renders the size badge when size is set', () => {
      renderCard(makeTicket({ size: 'm' }))
      expect(screen.getByTestId('size-badge')).toHaveTextContent('m')
    })

    it('does not render the size badge when size is null', () => {
      renderCard(makeTicket({ size: null }))
      expect(screen.queryByTestId('size-badge')).not.toBeInTheDocument()
    })

    it('renders tag chips for each tag', () => {
      renderCard(makeTicket({ tags: ['backend', 'auth'] }))
      const chips = screen.getAllByTestId('tag-chip')
      expect(chips).toHaveLength(2)
      expect(chips[0]).toHaveTextContent('backend')
      expect(chips[1]).toHaveTextContent('auth')
    })

    it('renders repo chips for each repo', () => {
      renderCard(makeTicket({ repos: ['org/repo', 'org/other'] }))
      const chips = screen.getAllByTestId('repo-chip')
      expect(chips).toHaveLength(2)
      expect(chips[0]).toHaveTextContent('org/repo')
    })

    it('renders the blocked badge when ticket.blocked is true', () => {
      renderCard(makeTicket({ blocked: true }))
      expect(screen.getByTestId('blocked-badge')).toBeInTheDocument()
    })

    it('does not render the blocked badge when ticket.blocked is false', () => {
      renderCard(makeTicket({ blocked: false }))
      expect(screen.queryByTestId('blocked-badge')).not.toBeInTheDocument()
    })
  })

  describe('bound-session indicator', () => {
    it('shows the session indicator when the bound session is waiting', () => {
      const ticket = makeTicket({ session_id: 'sess-abc' })
      const sessions = [makeSession('sess-abc', 'waiting')]
      renderCard(ticket, sessions)
      expect(screen.getByTestId('session-indicator')).toBeInTheDocument()
    })

    it('does not show the session indicator when the session is running', () => {
      const ticket = makeTicket({ session_id: 'sess-abc' })
      const sessions = [makeSession('sess-abc', 'running')]
      renderCard(ticket, sessions)
      expect(screen.queryByTestId('session-indicator')).not.toBeInTheDocument()
    })

    it('does not show the session indicator when there is no bound session', () => {
      renderCard(makeTicket({ session_id: null }))
      expect(screen.queryByTestId('session-indicator')).not.toBeInTheDocument()
    })
  })

  describe('navigation', () => {
    it('clicking the card navigates to the ticket detail URL', () => {
      renderCard(makeTicket({ id: 't42', board_id: 'b1' }))
      const card = screen.getByTestId('ticket-card')
      fireEvent.click(card)
      expect(screen.getByTestId('location')).toHaveTextContent('/boards/b1/ticket/t42')
    })
  })

  describe('Move to… control', () => {
    it('shows a "Move to…" button', () => {
      renderCard(makeTicket({ column_id: 'col-todo' }))
      expect(screen.getByTestId('move-to-button')).toBeInTheDocument()
    })

    it('clicking the button opens a menu with other columns', () => {
      renderCard(makeTicket({ column_id: 'col-todo' }))
      fireEvent.click(screen.getByTestId('move-to-button'))
      const menu = screen.getByTestId('move-menu')
      expect(menu).toBeInTheDocument()
      // Should show columns other than the current one
      expect(screen.getByText('In Progress')).toBeInTheDocument()
      expect(screen.getByText('Done')).toBeInTheDocument()
      // The current column (Todo) should not appear in the menu
      expect(screen.queryAllByRole('button', { name: 'Todo' }).filter(
        b => b.closest('[data-testid="move-menu"]')
      )).toHaveLength(0)
    })

    it('selecting a column from the menu calls moveTicket with the right args', async () => {
      renderCard(makeTicket({ id: 't1', column_id: 'col-todo' }))

      fireEvent.click(screen.getByTestId('move-to-button'))
      fireEvent.click(screen.getByTestId('move-to-col-in-progress'))

      await waitFor(() => {
        expect(boardApi.moveTicket).toHaveBeenCalledWith('t1', {
          columnId: 'col-in-progress',
          opId: expect.any(String),
        })
      })
    })

    it('closes the menu after selecting a column', async () => {
      renderCard(makeTicket({ id: 't1', column_id: 'col-todo' }))

      fireEvent.click(screen.getByTestId('move-to-button'))
      expect(screen.getByTestId('move-menu')).toBeInTheDocument()

      fireEvent.click(screen.getByTestId('move-to-col-in-progress'))

      await waitFor(() => {
        expect(screen.queryByTestId('move-menu')).not.toBeInTheDocument()
      })
    })

    it('clicking Move-to button does not navigate away from current page', () => {
      renderCard(makeTicket({ column_id: 'col-todo' }))
      fireEvent.click(screen.getByTestId('move-to-button'))
      expect(screen.getByTestId('location')).toHaveTextContent('/')
    })
  })
})
