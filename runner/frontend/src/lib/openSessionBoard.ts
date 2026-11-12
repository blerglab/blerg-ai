import type { Ticket } from '@blerglab/chat'
import { signedInAccount } from './liveBoardCard'
export const OPEN_SESSION_BOARD = 'blerg-open-session-board'
export function openSessionBoard(session: string, boardId: string, label: string): boolean {
 return !window.dispatchEvent(new CustomEvent(OPEN_SESSION_BOARD, { cancelable: true, detail: { session, boardId, label, account: signedInAccount() } }))
}

/** Read whether this chat has a mounted board tab host. */
export function hasSessionBoardHost(session: string): boolean {
 return !window.dispatchEvent(new CustomEvent(OPEN_SESSION_BOARD, { cancelable: true, detail: { session, probe: true, account: signedInAccount() } }))
}

export function openSessionTicket(session: string, ticket: Ticket): boolean {
 return !window.dispatchEvent(new CustomEvent(OPEN_SESSION_BOARD, { cancelable: true, detail: { session, boardId: ticket.board_id, ticket, account: signedInAccount() } }))
}
