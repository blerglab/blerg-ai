// A proposed board (blerg-runner board create): the access the session asks for on the new board.
import { isBoardLevel, type BoardLevel } from './permissionsModal'
import type { ProposalInfo } from '../types'

/** The access a board proposal asks for; full when the arguments do not say (the server default). */
export function requestedBoardAccess(p: ProposalInfo): BoardLevel {
  const v = p.arguments.access
  return isBoardLevel(v) && v !== 'propose' ? v : 'full'
}

/** What each level gives the session on the new board, said under the approval's choice. */
export const BOARD_ACCESS_HINT: Record<BoardLevel, string> = {
  propose: 'No access: the session proposes cards on it, each asking you.',
  read: 'The session can list and read every card.',
  file: 'The session can also file cards without asking.',
  full: 'The session can also create, update, move and comment.',
}
