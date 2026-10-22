// A proposed board (blerg-runner board create): what the session asks for on the new board, and the
// person's choice when approving. The person may lower the access, or give none, never raise it.
import { BOARD_LEVELS, BOARD_LEVEL_LABELS, boardLevelRank, type BoardLevel } from '../lib/permissionsModal'
import { BOARD_ACCESS_HINT, requestedBoardAccess } from '../lib/boardCreate'
import type { ProposalInfo } from '../types'

/** The approval's access choice: the requested level and every lower one ("None" is propose). */
export default function BoardCreateAccess({ proposal, value, onChange, disabled }: {
  proposal: ProposalInfo
  value: BoardLevel
  onChange: (level: BoardLevel) => void
  disabled?: boolean
}) {
  const requested = requestedBoardAccess(proposal)
  const id = `board-create-access-${proposal.id}`
  return <div className="board-create-access">
    <label htmlFor={id}>Session access to the new board</label>
    <select id={id} value={value} disabled={disabled} onChange={e => onChange(e.target.value as BoardLevel)}>
      {BOARD_LEVELS.filter(l => boardLevelRank(l) <= boardLevelRank(requested)).reverse().map(l =>
        <option key={l} value={l}>{l === 'propose' ? 'None (propose only)' : BOARD_LEVEL_LABELS[l]}{l === requested ? ' (asked for)' : ''}</option>)}
    </select>
    <p className="board-create-access-hint">{BOARD_ACCESS_HINT[value]} Only this session gets it.</p>
  </div>
}
