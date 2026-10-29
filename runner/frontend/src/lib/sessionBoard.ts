// The board an unqualified card reference names: the session's single created or pinned board,
// otherwise its only accessible board. Created-board and pin events update the choice during the
// session; multiple in-use boards require an explicit board:<id>#N reference.
import { createContext, useContext, useEffect, useMemo, useState } from 'react'
import { pinnedFromEvents, proposalStates, useTranscriptStore, type AgentEvent } from '@blerglab/chat'
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
// What one account read is never shown to another (a silent renewal can sign in someone else).
if (typeof window !== 'undefined') window.addEventListener('blerg-identity-changed', () => { held.clear(); inflight.clear(); epoch++ })
let epoch = 0

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

/** Boards actually used by the session take precedence over its broader permissions. */
export function inUseBoards(events: readonly AgentEvent[]): SessionBoard[] {
  const boards = new Map<string, SessionBoard>()
  for (const pin of pinnedFromEvents(events).tickets) {
    if (pin.source === 'blerg-board' && pin.boardId) boards.set(pin.boardId, { id: pin.boardId })
  }
  for (const p of proposalStates(events).values()) {
    if (p.state === 'done' && p.board && p.board.access !== 'propose') {
      boards.set(p.board.board_id, { id: p.board.board_id, name: p.board.board_name })
    }
  }
  return [...boards.values()].sort((a, b) => a.id.localeCompare(b.id))
}

const NO_EVENTS: readonly AgentEvent[] = []

function load(sessionId: string): Promise<SessionBoard | null> {
  let p = inflight.get(sessionId)
  if (!p) {
    const read = epoch
    p = apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/permissions`)
      .then(async r => (r.ok ? onlyBoard(await r.json()) : null), () => null)
      .then(b => { if (read === epoch) held.set(sessionId, b); return b })
      .finally(() => { if (inflight.get(sessionId) === p) inflight.delete(sessionId) })
    inflight.set(sessionId, p)
  }
  return p
}

/** Tracks in-use boards live, with a cached permissions fallback while active. */
export function useLoadSessionBoard(sessionId: string, active: boolean): SessionBoard | null {
  const events = useTranscriptStore(s => s.sessions[sessionId]?.events ?? NO_EVENTS)
  // Keep the result stable during ordinary chat events, so readers and their caches stay stable.
  const usedKey = useMemo(() => JSON.stringify(inUseBoards(events)), [events])
  const used = useMemo(() => JSON.parse(usedKey) as SessionBoard[], [usedKey])
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
  return used.length === 1 ? used[0] : used.length > 1 ? null
    : shown.sessionId === sessionId ? shown.board : (held.get(sessionId) ?? null)
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
