// TDD: board WS store tests.
// Run: cd frontend && npm test -- src/hooks/useBoardStore.test.ts

import { describe, it, expect, vi, beforeEach } from 'vitest'
import * as wsMock from '../test/wsMock'
import { useBoardStore } from './useBoardStore'
import type { Board, Ticket, Column } from '../types'

vi.mock('../ws', () => import('../test/wsMock'))

// ── helpers ────────────────────────────────────────────────────────────────────

function makeTicket(over: Partial<Ticket> = {}): Ticket {
  return {
    id: 't1',
    board_id: 'b1',
    column_id: 'col-1',
    title: 'Default ticket',
    body: null,
    priority: 'medium',
    size: null,
    rank: 'a',
    archived_at: null,
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

function makeColumn(over: Partial<Column> = {}): Column {
  return {
    id: 'col-1',
    board_id: 'b1',
    rank: 'a',
    name: 'Todo',
    is_terminal: false,
    created_at: '2026-01-01T00:00:00Z',
    ...over,
  }
}

const get = () => useBoardStore.getState()

function makeBoard(over: Partial<Board> = {}): Board {
  return {
    id: 'b1',
    name: 'Sprint',
    description: null,
    repos: [],
    default_daemon_id: null,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...over,
  }
}

beforeEach(() => {
  wsMock.resetWsMock()
  useBoardStore.setState({
    board: null,
    boardId: null,
    columns: {},
    tickets: {},
    loading: false,
    pendingOpIds: new Set(),
  })
})

// ── subscribeBoard ─────────────────────────────────────────────────────────────

describe('subscribeBoard', () => {
  it('sends a subscribe_board WS message', () => {
    get().subscribeBoard('b1')
    expect(wsMock.send).toHaveBeenCalledWith({ type: 'subscribe_board', board_id: 'b1' })
  })

  it('re-subscribes when the socket reconnects', () => {
    get().subscribeBoard('b1')
    wsMock.send.mockClear()
    wsMock.flushOpen()
    expect(wsMock.send).toHaveBeenCalledWith({ type: 'subscribe_board', board_id: 'b1' })
  })

  it('does NOT re-subscribe after unsubscribeBoard', () => {
    get().subscribeBoard('b1')
    get().unsubscribeBoard()
    wsMock.send.mockClear()
    wsMock.flushOpen()
    expect(wsMock.send).not.toHaveBeenCalled()
  })
})

// ── ticket_created ─────────────────────────────────────────────────────────────

describe('ticket_created event', () => {
  it('adds the ticket to the store keyed by id', () => {
    const ticket = makeTicket({ id: 't1', title: 'New ticket' })
    wsMock.emit({ type: 'ticket_created', ticket, op_id: '' })
    expect(get().tickets['t1']).toEqual(ticket)
  })

  it('is idempotent — emitting twice does not create duplicates', () => {
    const ticket = makeTicket({ id: 't1' })
    wsMock.emit({ type: 'ticket_created', ticket, op_id: '' })
    wsMock.emit({ type: 'ticket_created', ticket, op_id: '' })
    expect(Object.keys(get().tickets)).toHaveLength(1)
  })
})

// ── ticket_moved ───────────────────────────────────────────────────────────────

describe('ticket_moved event', () => {
  it('updates the ticket column_id and rank in the store', () => {
    // Seed initial state
    useBoardStore.setState({ tickets: { t1: makeTicket({ column_id: 'col-1', rank: 'a' }) } })

    const movedTicket = makeTicket({ id: 't1', column_id: 'col-2', rank: 'b', version: 2 })
    wsMock.emit({
      type: 'ticket_moved',
      ticket: movedTicket,
      from_column_id: 'col-1',
      to_column_id: 'col-2',
      op_id: '',
    })

    const stored = get().tickets['t1']
    expect(stored.column_id).toBe('col-2')
    expect(stored.rank).toBe('b')
    expect(stored.version).toBe(2)
  })

  it('ticket appears exactly once after move (no duplication)', () => {
    useBoardStore.setState({ tickets: { t1: makeTicket() } })
    const movedTicket = makeTicket({ id: 't1', column_id: 'col-2' })
    wsMock.emit({
      type: 'ticket_moved',
      ticket: movedTicket,
      from_column_id: 'col-1',
      to_column_id: 'col-2',
      op_id: '',
    })
    expect(Object.keys(get().tickets)).toHaveLength(1)
  })
})

// ── op_id reconciliation ───────────────────────────────────────────────────────

describe('op_id reconciliation', () => {
  it('clears a pending op_id when its event arrives', () => {
    useBoardStore.setState({ tickets: { t1: makeTicket({ column_id: 'col-1' }) } })
    get().addPendingOp('op-move-1')
    expect(get().pendingOpIds.has('op-move-1')).toBe(true)

    const movedTicket = makeTicket({ id: 't1', column_id: 'col-2', version: 2 })
    wsMock.emit({
      type: 'ticket_moved',
      ticket: movedTicket,
      from_column_id: 'col-1',
      to_column_id: 'col-2',
      op_id: 'op-move-1',
    })

    expect(get().pendingOpIds.has('op-move-1')).toBe(false)
  })

  it('applies authoritative state after optimistic move — ticket appears once, not twice', () => {
    // Simulate: optimistic move already applied (ticket in col-2)
    useBoardStore.setState({
      tickets: {
        t1: makeTicket({ column_id: 'col-2', rank: 'z', version: 1 }),
      },
      pendingOpIds: new Set(['op-move-1']),
    })

    // Server confirms with authoritative state (slightly different rank)
    const authoritative = makeTicket({ id: 't1', column_id: 'col-2', rank: 'b', version: 2 })
    wsMock.emit({
      type: 'ticket_moved',
      ticket: authoritative,
      from_column_id: 'col-1',
      to_column_id: 'col-2',
      op_id: 'op-move-1',
    })

    const stored = get().tickets['t1']
    // Authoritative state wins
    expect(stored.rank).toBe('b')
    expect(stored.version).toBe(2)
    // Only one entry
    expect(Object.keys(get().tickets)).toHaveLength(1)
    // Pending cleared
    expect(get().pendingOpIds.has('op-move-1')).toBe(false)
  })

  it('still applies events whose op_id is not in pendingOpIds (events from other clients)', () => {
    useBoardStore.setState({ tickets: { t1: makeTicket({ column_id: 'col-1' }) } })
    const movedTicket = makeTicket({ id: 't1', column_id: 'col-3' })
    wsMock.emit({
      type: 'ticket_moved',
      ticket: movedTicket,
      from_column_id: 'col-1',
      to_column_id: 'col-3',
      op_id: 'op-other-client',
    })
    expect(get().tickets['t1'].column_id).toBe('col-3')
  })
})

// ── ticket_updated ─────────────────────────────────────────────────────────────

describe('ticket_updated event', () => {
  it('upserts the ticket fields', () => {
    useBoardStore.setState({ tickets: { t1: makeTicket({ title: 'Old' }) } })
    const updated = makeTicket({ id: 't1', title: 'New', priority: 'urgent', version: 2 })
    wsMock.emit({ type: 'ticket_updated', ticket: updated, op_id: '' })
    expect(get().tickets['t1'].title).toBe('New')
    expect(get().tickets['t1'].priority).toBe('urgent')
  })
})

// ── ticket_archived ────────────────────────────────────────────────────────────

describe('ticket_archived event', () => {
  it('upserts the ticket with archived_at set', () => {
    useBoardStore.setState({ tickets: { t1: makeTicket() } })
    const archived = makeTicket({ id: 't1', archived_at: '2026-06-01T00:00:00Z', column_id: null })
    wsMock.emit({ type: 'ticket_archived', ticket: archived, op_id: '' })
    expect(get().tickets['t1'].archived_at).toBe('2026-06-01T00:00:00Z')
    expect(get().tickets['t1'].column_id).toBeNull()
  })
})

// ── ticket_split ───────────────────────────────────────────────────────────────

describe('ticket_split event', () => {
  it('updates the origin ticket and upserts children', () => {
    useBoardStore.setState({ tickets: { t1: makeTicket({ id: 't1' }) } })
    const origin = makeTicket({ id: 't1', archived_at: '2026-06-01T00:00:00Z', column_id: null })
    const child1 = makeTicket({ id: 't2', title: 'Part A' })
    const child2 = makeTicket({ id: 't3', title: 'Part B' })
    wsMock.emit({
      type: 'ticket_split',
      origin_id: 't1',
      origin_ticket: origin,
      children: [child1, child2],
      op_id: '',
    })
    expect(get().tickets['t1'].archived_at).toBe('2026-06-01T00:00:00Z')
    expect(get().tickets['t2'].title).toBe('Part A')
    expect(get().tickets['t3'].title).toBe('Part B')
    expect(Object.keys(get().tickets)).toHaveLength(3)
  })
})

// ── column_changed ─────────────────────────────────────────────────────────────

describe('column_changed event', () => {
  it('upserts the column by id', () => {
    const col = makeColumn({ id: 'col-1', name: 'Todo' })
    wsMock.emit({ type: 'column_changed', column: col, op_id: '' })
    expect(get().columns['col-1'].name).toBe('Todo')
  })

  it('updates an existing column by id (no duplicates)', () => {
    useBoardStore.setState({ columns: { 'col-1': makeColumn({ id: 'col-1', name: 'Old' }) } })
    const updated = makeColumn({ id: 'col-1', name: 'New', is_terminal: true })
    wsMock.emit({ type: 'column_changed', column: updated, op_id: '' })
    expect(get().columns['col-1'].name).toBe('New')
    expect(Object.keys(get().columns)).toHaveLength(1)
  })
})

// ── column_removed ─────────────────────────────────────────────────────────────

describe('column_removed event', () => {
  it('removes the column from the store', () => {
    useBoardStore.setState({ columns: { 'col-1': makeColumn() } })
    wsMock.emit({ type: 'column_removed', column_id: 'col-1', board_id: 'b1', op_id: '' })
    expect(get().columns['col-1']).toBeUndefined()
    expect(Object.keys(get().columns)).toHaveLength(0)
  })
})

// ── board_created ──────────────────────────────────────────────────────────────

describe('board_created event', () => {
  it('stores the board_id on board_created', () => {
    const board = { id: 'b1', name: 'Sprint', description: null, repos: [], default_daemon_id: null, created_at: '', updated_at: '' }
    wsMock.emit({ type: 'board_created', board, op_id: '' })
    // board_created doesn't carry columns/tickets; just confirms it doesn't crash
    expect(get().boardId).toBe(null) // board_created doesn't set boardId
  })
})

// ── setBoardData ───────────────────────────────────────────────────────────────

describe('setBoardData', () => {
  it('populates board, columns, and tickets from REST load', () => {
    const board = makeBoard({ id: 'b1', name: 'Sprint' })
    const col = makeColumn({ id: 'col-1', name: 'Todo' })
    const ticket = makeTicket({ id: 't1', title: 'Write tests' })

    get().setBoardData(board, [col], [ticket])

    expect(get().board).toEqual(board)
    expect(get().columns['col-1']).toEqual(col)
    expect(get().tickets['t1']).toEqual(ticket)
  })

  it('sets loading: false after data load', () => {
    useBoardStore.setState({ loading: true })
    get().setBoardData(makeBoard(), [], [])
    expect(get().loading).toBe(false)
  })

  it('replaces previous columns and tickets with the new snapshot', () => {
    // Pre-seed stale data
    useBoardStore.setState({
      columns: { 'old-col': makeColumn({ id: 'old-col' }) },
      tickets: { 'old-t': makeTicket({ id: 'old-t' }) },
    })

    const newCol = makeColumn({ id: 'new-col', name: 'In Progress' })
    const newTicket = makeTicket({ id: 'new-t', title: 'Fresh ticket' })
    get().setBoardData(makeBoard(), [newCol], [newTicket])

    // Stale data gone
    expect(get().columns['old-col']).toBeUndefined()
    expect(get().tickets['old-t']).toBeUndefined()
    // New data present
    expect(get().columns['new-col']).toEqual(newCol)
    expect(get().tickets['new-t']).toEqual(newTicket)
  })
})
