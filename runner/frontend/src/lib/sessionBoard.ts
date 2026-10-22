// The board a card chip names when the chip itself names none (card #139): an imported mark, a
// `mark --card N` line without a board, a pinned ticket of a board session. The chip's board is never
// written in at mark time (#115); at render time, when the session has access to exactly one board
// (its permissions' Boards: the launch board or one given since), that board is the one the chip
// means. With none, or several, the chip says the board is unknown. Read once per session and kept.
import { createContext, useContext, useEffect, useState } from 'react'
import { apiFetch } from '../apiFetch'
import { readPermissions } from './sessionPermissions'

export interface SessionBoard { id: string; name?: string }

/** What the panel knows: the session's one board, or null (none, several, or not readable). */
export const SessionBoardContext = createContext<SessionBoard | null>(null)

/** The board a chip without one means here, or null. */
export function useSessionBoard(): SessionBoard | null {
  return useContext(SessionBoardContext)
}

const held = new Map<string, SessionBoard | null>()
const inflight = new Map<string, Promise<SessionBoard | null>>()

/** The one board of a permissions reply, else null. */
export function onlyBoard(body: unknown): SessionBoard | null {
  try {
    const boards = readPermissions(body).boards.filter(b => b.board_id)
    const ids = new Set(boards.map(b => b.board_id))
    if (ids.size !== 1) return null
    const b = boards[0]
    return { id: b.board_id, ...(b.board_name ? { name: b.board_name } : {}) }
  } catch {
    return null
  }
}

function load(sessionId: string): Promise<SessionBoard | null> {
  let p = inflight.get(sessionId)
  if (!p) {
    p = apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/permissions`)
      .then(async r => (r.ok ? onlyBoard(await r.json()) : null), () => null)
      .then(b => { held.set(sessionId, b); return b })
      .finally(() => inflight.delete(sessionId))
    inflight.set(sessionId, p)
  }
  return p
}

/** Reads the session's one board while `active` (once per session; kept for the page's life). */
export function useLoadSessionBoard(sessionId: string, active: boolean): SessionBoard | null {
  const [shown, setShown] = useState<{ sessionId: string; board: SessionBoard | null }>(() => ({ sessionId, board: held.get(sessionId) ?? null }))
  useEffect(() => {
    if (!active) return
    let live = true
    if (held.has(sessionId)) {
      const b = held.get(sessionId) ?? null
      Promise.resolve().then(() => { if (live) setShown({ sessionId, board: b }) })
    } else {
      void load(sessionId).then(b => { if (live) setShown({ sessionId, board: b }) })
    }
    return () => { live = false }
  }, [sessionId, active])
  return shown.sessionId === sessionId ? shown.board : (held.get(sessionId) ?? null)
}

/** For tests: forget what was read. */
export function resetSessionBoardsForTest(): void {
  held.clear()
  inflight.clear()
}

/** The hovercard of a card chip whose board is not known: the number, and that the board is not. */
export function unknownBoardTarget(number: number) {
  return { kind: 'card' as const, source: 'Card', key: `#${number}`, status: 'board unknown' }
}

/** A blerg-board ticket's board: its own, else the session's one board. */
export function withSessionBoard<T extends { source: string; boardId?: string }>(t: T, board: SessionBoard | null): T {
  if (t.boardId || !board || t.source !== 'blerg-board') return t
  return { ...t, boardId: board.id }
}
