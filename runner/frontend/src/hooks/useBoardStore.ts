// Board WS store — mirrors the shape of useSessionStore.ts.
// Registers onMessage handlers once at store creation; re-subscribes on onOpen.

import { useMemo } from 'react'
import { create } from 'zustand'
import type {
  Board,
  Column,
  Ticket,
  BoardCreated,
  ColumnChanged,
  ColumnRemoved,
  TicketCreated,
  TicketUpdated,
  TicketMoved,
  TicketSplit,
  TicketArchived,
  TicketDependencyChanged,
} from '../types'
import { onMessage, onOpen, send } from '../ws'

interface BoardStore {
  /** The currently loaded board (null when not loaded). */
  board: Board | null
  /** Currently subscribed board id (null when not subscribed). */
  boardId: string | null
  /** Columns keyed by column id — upsert by id, never push to array. */
  columns: Record<string, Column>
  /** Tickets keyed by ticket id — upsert by id, never push to array. */
  tickets: Record<string, Ticket>
  /** True while the initial board load is in flight. */
  loading: boolean
  /**
   * op_ids for in-flight optimistic mutations. When a WS event arrives whose
   * op_id matches an entry here, the store applies the authoritative state and
   * removes the entry — confirming the mutation without double-applying.
   */
  pendingOpIds: Set<string>

  /** Subscribe to WS events for boardId and re-subscribe on reconnect. */
  subscribeBoard: (boardId: string) => void
  /** Unsubscribe from WS events for the current board. */
  unsubscribeBoard: () => void
  /** Record an outgoing mutation's op_id so the echo can be reconciled. */
  addPendingOp: (opId: string) => void
  /** Explicitly remove a pending op_id (e.g. on request error). */
  removePendingOp: (opId: string) => void
  /**
   * Populate the store with the REST load result (board + columns + tickets).
   * Called by BoardView after getBoard() resolves. Sets loading: false.
   */
  setBoardData: (board: Board, columns: Column[], tickets: Ticket[]) => void
}

// ── helpers ────────────────────────────────────────────────────────────────────

/** Upsert a ticket into the record, returning a new object. */
function upsertTicket(tickets: Record<string, Ticket>, ticket: Ticket): Record<string, Ticket> {
  return { ...tickets, [ticket.id]: ticket }
}

/** Upsert a column into the record, returning a new object. */
function upsertColumn(columns: Record<string, Column>, column: Column): Record<string, Column> {
  return { ...columns, [column.id]: column }
}

/**
 * Clear op_id from pending set if present, returning the new set.
 * If not present, returns the same Set instance (no re-render).
 */
function clearPending(pending: Set<string>, opId: string): Set<string> {
  if (!opId || !pending.has(opId)) return pending
  const next = new Set(pending)
  next.delete(opId)
  return next
}

// ── store ──────────────────────────────────────────────────────────────────────

export const useBoardStore = create<BoardStore>((set, get) => {
  // Track the cancel function for the onOpen handler so we can deregister it
  // when the user unsubscribes (prevents re-subscription after cleanup).
  let cancelOnOpen: (() => void) | null = null

  // ── WS event handlers (registered once at module load) ─────────────────────

  onMessage<BoardCreated>('board_created', () => {
    // board_created doesn't carry columns/tickets; no store update needed here.
    // Components that need to react can subscribe to this event directly.
  })

  onMessage<ColumnChanged>('column_changed', (msg) => {
    set((state) => ({
      columns: upsertColumn(state.columns, msg.column),
      pendingOpIds: clearPending(state.pendingOpIds, msg.op_id),
    }))
  })

  onMessage<ColumnRemoved>('column_removed', (msg) => {
    set((state) => {
      // eslint-disable-next-line @typescript-eslint/no-unused-vars
      const { [msg.column_id]: _removed, ...rest } = state.columns
      return {
        columns: rest,
        pendingOpIds: clearPending(state.pendingOpIds, msg.op_id),
      }
    })
  })

  onMessage<TicketCreated>('ticket_created', (msg) => {
    set((state) => ({
      tickets: upsertTicket(state.tickets, msg.ticket),
      pendingOpIds: clearPending(state.pendingOpIds, msg.op_id),
    }))
  })

  onMessage<TicketUpdated>('ticket_updated', (msg) => {
    set((state) => ({
      tickets: upsertTicket(state.tickets, msg.ticket),
      pendingOpIds: clearPending(state.pendingOpIds, msg.op_id),
    }))
  })

  onMessage<TicketMoved>('ticket_moved', (msg) => {
    set((state) => ({
      tickets: upsertTicket(state.tickets, msg.ticket),
      pendingOpIds: clearPending(state.pendingOpIds, msg.op_id),
    }))
  })

  onMessage<TicketSplit>('ticket_split', (msg) => {
    set((state) => {
      let tickets = upsertTicket(state.tickets, msg.origin_ticket)
      for (const child of msg.children) {
        tickets = upsertTicket(tickets, child)
      }
      return {
        tickets,
        pendingOpIds: clearPending(state.pendingOpIds, msg.op_id),
      }
    })
  })

  onMessage<TicketArchived>('ticket_archived', (msg) => {
    set((state) => ({
      tickets: upsertTicket(state.tickets, msg.ticket),
      pendingOpIds: clearPending(state.pendingOpIds, msg.op_id),
    }))
  })

  onMessage<TicketDependencyChanged>('ticket_dependency_changed', (msg) => {
    // The server also emits a ticket_updated with the recomputed blocked state;
    // the dependency_changed event itself only carries the edge — no ticket upsert
    // needed here, just clear the pending op_id if present.
    set((state) => ({
      pendingOpIds: clearPending(state.pendingOpIds, msg.op_id),
    }))
  })

  // ── store methods ───────────────────────────────────────────────────────────

  return {
    board: null,
    boardId: null,
    columns: {},
    tickets: {},
    loading: false,
    pendingOpIds: new Set(),

    subscribeBoard(boardId: string) {
      // Cancel any previous onOpen registration before registering a new one,
      // so swapping boards doesn't accumulate stale handlers.
      if (cancelOnOpen) {
        cancelOnOpen()
        cancelOnOpen = null
      }

      set({ boardId })
      send({ type: 'subscribe_board', board_id: boardId })

      // Re-subscribe on every reconnect while this board is active.
      cancelOnOpen = onOpen(() => {
        const currentId = get().boardId
        if (currentId) {
          send({ type: 'subscribe_board', board_id: currentId })
        }
      })
    },

    unsubscribeBoard() {
      const { boardId } = get()
      if (cancelOnOpen) {
        cancelOnOpen()
        cancelOnOpen = null
      }
      if (boardId) {
        send({ type: 'unsubscribe_board', board_id: boardId })
      }
      set({ boardId: null, board: null, columns: {}, tickets: {} })
    },

    addPendingOp(opId: string) {
      set((state) => {
        const next = new Set(state.pendingOpIds)
        next.add(opId)
        return { pendingOpIds: next }
      })
    },

    removePendingOp(opId: string) {
      set((state) => ({
        pendingOpIds: clearPending(state.pendingOpIds, opId),
      }))
    },

    setBoardData(board: Board, columns: Column[], tickets: Ticket[]) {
      const colsMap: Record<string, Column> = {}
      for (const c of columns) colsMap[c.id] = c
      const ticketsMap: Record<string, Ticket> = {}
      for (const t of tickets) ticketsMap[t.id] = t
      set({ board, columns: colsMap, tickets: ticketsMap, loading: false })
    },
  }
})

// ── useBoard selector hook ─────────────────────────────────────────────────────

/** Returns the current board, its columns and tickets as arrays, plus loading state. */
export function useBoard(_boardId: string) {
  // Select the raw Records by reference — they're stable between store updates.
  const board = useBoardStore((s) => s.board)
  const loading = useBoardStore((s) => s.loading)
  const columnsRecord = useBoardStore((s) => s.columns)
  const ticketsRecord = useBoardStore((s) => s.tickets)

  // Convert Records → arrays only when the Record reference changes.
  // Object.values() always creates a new array, so calling it directly in a Zustand
  // selector causes useSyncExternalStore to see a new snapshot on every render and
  // loop infinitely. Memoising behind the Record reference avoids this.
  const columns = useMemo(() => Object.values(columnsRecord), [columnsRecord])
  const tickets = useMemo(() => Object.values(ticketsRecord), [ticketsRecord])

  return { board, columns, tickets, loading }
}
