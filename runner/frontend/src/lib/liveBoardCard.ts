import { useCallback, useEffect, useState } from 'react'
import { apiFetch } from '../apiFetch'
import { getAccessToken } from '../authClient'
import { tokenSubject } from '../claims'

// A pinned Blerg Board card, read live from the board with the signed-in person's own permissions
// (the runner's GET /api/board-cards/{board}/{number}, board_cards_live.go): what the panel's chip
// and the ticket dialog show in place of the agent's copy, which goes stale as soon as the card
// moves. Reads are shared between the chip and the dialog, and kept briefly.

export interface LiveBoardColumn { id: string; name: string; is_terminal?: boolean }
export interface LiveBoardTrailEvent { type: string; actor: string; to_column?: string; text?: string; created_at: string }
export interface LiveBoardOrigin { session_url?: string; repo?: string; commit?: string; engine?: string }
export interface LiveBoardCard {
  id: string; board_id: string; number: number; title: string; body?: string | null; type?: string; priority?: string
  size?: string | null; column_id?: string | null; column_name?: string; tags?: string[] | null; repos?: string[] | null
  archived_at?: string | null; sensitive?: boolean; origin?: LiveBoardOrigin | null; version?: number; updated_at?: string
  columns?: LiveBoardColumn[] | null; trail?: LiveBoardTrailEvent[] | null
}
export interface LiveBoardRead { card: LiveBoardCard; url: string }

/** The board and number of a pinned Blerg Board card, or null for any other ticket. */
export function boardCardRef(t: { source: string; key: string; boardId?: string; board_id?: string }): { board: string; number: number } | null {
  const board = t.boardId ?? t.board_id
  if (t.source !== 'blerg-board' || !board) return null
  const number = Number(t.key.replace(/^#/, ''))
  return Number.isSafeInteger(number) && number > 0 ? { board, number } : null
}

/** How long a read stands for the chip before it is read again. */
const FRESH_MS = 30_000
const reads = new Map<string, { at: number; read: LiveBoardRead }>()
const inflight = new Map<string, Promise<LiveBoardRead>>()
const listeners = new Set<() => void>()
// Reads are the person's (their own board permissions): kept per account, and all forgotten when
// the tab's account changes (a silent renewal can sign in someone else without a reload), so one
// person never sees a card read under another's sign-in.
let heldFor = ''
function account(): string {
  const who = tokenSubject(getAccessToken())
  if (who !== heldFor) {
    reads.clear()
    inflight.clear()
    heldFor = who
  }
  return who
}
const keyOf = (board: string, number: number) => `${account()}|${board}#${number}`

/** Test hook: forget every read. */
export function resetLiveBoardCardsForTest(): void { reads.clear(); inflight.clear(); heldFor = '' }

export class LiveBoardCardError extends Error {
  readonly status: number
  constructor(message: string, status: number) { super(message); this.status = status }
}

async function reply(res: Response): Promise<LiveBoardRead> {
  let body: unknown = null
  try { body = await res.json() } catch { /* not JSON */ }
  if (!res.ok) {
    const msg = body && typeof body === 'object' && 'error' in body && typeof body.error === 'string' ? body.error : 'The board card could not be read.'
    throw new LiveBoardCardError(msg, res.status)
  }
  const r = body as Partial<LiveBoardRead> | null
  if (!r || typeof r.card !== 'object' || r.card === null || typeof r.card.title !== 'string') throw new LiveBoardCardError('The board returned an unreadable card.', 502)
  return { card: r.card, url: typeof r.url === 'string' ? r.url : '' }
}

function remember(key: string, read: LiveBoardRead) {
  if (!key.startsWith(`${account()}|`)) return // read under an account the tab no longer holds
  reads.set(key, { at: Date.now(), read })
  for (const l of listeners) l()
}

/** Reads the card now (a read in flight is shared). sessionId names the session for the audit line. */
export function readLiveBoardCard(board: string, number: number, sessionId?: string): Promise<LiveBoardRead> {
  const key = keyOf(board, number)
  const running = inflight.get(key)
  if (running) return running
  const q = sessionId ? `?session=${encodeURIComponent(sessionId)}` : ''
  const p = apiFetch(`/api/board-cards/${encodeURIComponent(board)}/${number}${q}`).then(reply).then(read => {
    remember(key, read)
    return read
  }).finally(() => inflight.delete(key))
  inflight.set(key, p)
  return p
}

/** Moves the card to column (as the person's own role allows): one credential exchange, no read
 *  after. The card held is moved in place (its column; the trail comes with the next read). */
export async function moveLiveBoardCard(board: string, number: number, column: string, sessionId?: string): Promise<void> {
  const key = keyOf(board, number)
  const q = sessionId ? `?session=${encodeURIComponent(sessionId)}` : ''
  const res = await apiFetch(`/api/board-cards/${encodeURIComponent(board)}/${number}/move${q}`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ column_id: column }),
  })
  let body: unknown = null
  try { body = await res.json() } catch { /* not JSON */ }
  if (!res.ok) {
    const msg = body && typeof body === 'object' && 'error' in body && typeof body.error === 'string' ? body.error : 'The card was not moved.'
    throw new LiveBoardCardError(msg, res.status)
  }
  const held = reads.get(key)
  if (held) {
    const name = held.read.card.columns?.find(c => c.id === column)?.name ?? held.read.card.column_name
    remember(key, { ...held.read, card: { ...held.read.card, column_id: column, column_name: name } })
  }
}

export type LiveBoardState =
  | { state: 'loading'; read: LiveBoardRead | null }
  | { state: 'ready'; read: LiveBoardRead }
  | { state: 'failed'; read: LiveBoardRead | null; error: string; status: number }

/** The live card for a pin (null ref: none, nothing is read). refresh reads it again now. */
export function useLiveBoardCard(ref: { board: string; number: number } | null, sessionId?: string, enabled = true): LiveBoardState & { refresh: () => void } {
  const board = ref?.board ?? ''
  const number = ref?.number ?? 0
  const key = board ? keyOf(board, number) : ''
  // What was read for which card: a state of another card (the pin changed) is never shown.
  const [held, setHeld] = useState<{ key: string; state: LiveBoardState } | null>(null)
  const [attempt, setAttempt] = useState(0)
  useEffect(() => {
    if (!key) return
    const sync = () => { const c = reads.get(key); if (c) setHeld({ key, state: { state: 'ready', read: c.read } }) }
    listeners.add(sync)
    return () => { listeners.delete(sync) }
  }, [key])
  useEffect(() => {
    if (!key || !enabled) return
    let active = true
    const c = reads.get(key)
    if (c && attempt === 0 && Date.now() - c.at < FRESH_MS) return
    readLiveBoardCard(board, number, sessionId).then(
      read => { if (active) setHeld({ key, state: { state: 'ready', read } }) },
      (e: unknown) => {
        if (!active) return
        const err = e instanceof LiveBoardCardError ? e : new LiveBoardCardError('The board card could not be read.', 0)
        setHeld(h => ({ key, state: { state: 'failed', read: h?.key === key ? h.state.read : reads.get(key)?.read ?? null, error: err.message, status: err.status } }))
      },
    )
    return () => { active = false }
  }, [key, board, number, sessionId, enabled, attempt])
  const refresh = useCallback(() => setAttempt(a => a + 1), [])
  const cachedRead = key ? reads.get(key)?.read ?? null : null
  const state: LiveBoardState = held?.key === key ? held.state : cachedRead ? { state: 'ready', read: cachedRead } : { state: 'loading', read: null }
  return { ...state, refresh }
}

/** What the panel's chip shows of a pin: the live column and title of a board card once read,
 *  else what the agent pinned. */
export function useTicketView(t: { source: string; key: string; boardId?: string; status: string; title: string }, sessionId: string): { status: string; title: string; live: boolean } {
  const ref = boardCardRef(t)
  const live = useLiveBoardCard(ref, sessionId)
  const card = ref && live.read ? live.read.card : null
  if (!card) return { status: t.status, title: t.title, live: false }
  return { status: card.archived_at ? 'Archived' : card.column_name || t.status, title: card.title || t.title, live: true }
}
