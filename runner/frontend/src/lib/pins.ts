// The session's pinned tickets, and the card a worker is on. Pure: SessionRail passes in the pins
// (newest first), the live board reads and the checklist.
//
// Every pin has one of three states (card #134), from the board column it sits in:
//   - done: a terminal column (the board marks it), an archived card, or a column whose name means
//     done or closed (Done, Closed, Merged, Won't do…: statusTone "done" or "canceled");
//   - in progress: a column whose name means being worked (In progress, In review, Doing, Testing,
//     Blocked…: statusTone "active" or "blocked"; a blocked card is still someone's work), or one
//     of the board names agents use for it (Review, Code review, In dev, Dev, Implementing, WIP,
//     QA, Verify, Staging); numbering, emoji and counts around a name are ignored ("2. In progress
//     (3)", "🚧 Doing");
//   - not started: anything else (Inbox, Backlog, Ready, To do, a name not known).
// A pin a worker is on right now (a running subagent or started session assigned to it through the
// checklist, or a started session that pinned it) is in progress whatever its column says.
// A card of another tracker (Jira, GitHub…) has no column the runner can read: its state comes the
// same way from the status the agent pinned it with. Live lists only the pins in progress (a done
// card leaves it as it closes, one not started is not there yet); Plan lists them all, one glyph each.
import { pinStateOfStatus as stateOfStatus, type ChecklistItem, type PinState, type TicketSummaryLive } from '@blerglab/chat'
import type { LiveBoardCard } from './liveBoardCard'

/** How many pins Live lists before "+N more". */
export const PINS_SHOWN = 3

// The pin states, the status-word reading and the glyphs are the chat's (the chat's pin rows draw them
// too): one definition for the panel and the transcript.
export type { PinState } from '@blerglab/chat'
export { bareStatus, pinStateOfStatus as stateOfStatus, PIN_STATE_GLYPH as STATE_GLYPH } from '@blerglab/chat'

/** A pin as the panel draws it: the board's column and title once read. */
export interface PinView {
  pin: TicketSummaryLive
  title: string
  /** The status in words (the board's column, else the agent's copy): tooltips, aria, hovercard. */
  status: string
  /** Read from the board (else the agent's copy). */
  live: boolean
  state: PinState
  done: boolean
  /** A Blerg Board card whose board read has not landed yet: the pinned state stands for now. */
  pending?: boolean
}

/** A board card's state (✓ ◐ ○) from its live read: its column, a terminal column, archived. */
export function cardState(card: LiveBoardCard): PinState {
  if (card.archived_at) return 'done'
  if (card.columns?.find(c => c.id === card.column_id)?.is_terminal) return 'done'
  return stateOfStatus(card.column_name)
}

/** What the panel shows of one pin. */
export function pinView(pin: TicketSummaryLive, card: LiveBoardCard | null | undefined): PinView {
  const state = card ? cardState(card) : stateOfStatus(pin.status)
  const status = card ? (card.archived_at ? 'Archived' : card.column_name || pin.status) : pin.status
  return { pin, title: card ? card.title || pin.title : pin.title, status, live: !!card, state, done: state === 'done' }
}

/** A worker on the pin right now makes it in progress, whatever its column. */
export function withWorkers(views: readonly PinView[], working: (v: PinView) => boolean): PinView[] {
  return views.map(v => (working(v) && v.state !== 'doing' ? { ...v, state: 'doing' as const, done: false } : v))
}

/** The pins Live lists, newest first: the ones in progress only. A board card still waiting for
 *  its read is held back (`waitOver` false), so a card already done never flashes on Live. */
export function livePins(views: readonly PinView[], waitOver = true): PinView[] {
  return views.filter(v => v.state === 'doing' && (!v.pending || waitOver))
}


function escape(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

/** Where `text` names the ticket `key` as a word ("#122" but not "#1220"; "WEB-12" and "#WEB-12"
 *  but not "WEB-123" or "WEB-12-3"), or -1. */
export function mentionAt(text: string, key: string): number {
  const k = key.trim()
  if (!k) return -1
  const before = k.startsWith('#') ? '[^\\w#]' : '[^\\w]'
  const m = new RegExp(`(^|${before})${escape(k)}(?![\\w]|-\\w)`, 'i').exec(text)
  return m ? m.index + m[1].length : -1
}

export function mentions(text: string, key: string): boolean {
  return mentionAt(text, key) >= 0
}

/** A started session's own pin, as the parent's live state reports it. */
export interface ChildPin { source: string; key: string; title: string; status: string; board_id?: string; url?: string }

/** The card a worker is on: its started session's own pin (an explicit link), else the pinned
 *  ticket its checklist item names ("Build History fold · #122"). */
export function workTicket(item: Pick<ChecklistItem, 'text'> | null | undefined, pins: readonly TicketSummaryLive[], own?: ChildPin | null): TicketSummaryLive | null {
  if (own && own.key) {
    const mine = pins.find(p => p.source === own.source && p.key === own.key && (p.boardId ?? '') === (own.board_id ?? ''))
    return mine ?? { source: own.source, key: own.key, title: own.title, status: own.status, url: own.url ?? '', ...(own.board_id ? { boardId: own.board_id } : {}), eventId: '', updatedAt: null }
  }
  if (!item) return null
  // The first one the item names.
  let best: TicketSummaryLive | null = null
  let at = Infinity
  for (const p of pins) {
    const i = mentionAt(item.text, p.key)
    if (i >= 0 && i < at) { best = p; at = i }
  }
  return best
}
