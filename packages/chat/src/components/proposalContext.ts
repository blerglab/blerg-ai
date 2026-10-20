// What a proposal card needs from its chat: the decisions seen in the transcript and the host's ways
// to read and decide a proposal, edit a board draft, open a created card and the session's
// permissions. Apart from ChatContext so a decision redraws the proposal cards only.
import { createContext, useContext } from 'react'
import type { FiledCard, ProposalDecidedPayload, ProposalDetail } from '../types'

export interface ProposalDecision {
  /** The exact stored draft that was reviewed (board cards). */
  expected?: string
  /** Also approve this session's later proposals of exactly this scope without asking. */
  allowForSession?: boolean
}

export interface ProposalContextValue {
  /** By proposal_id, from the transcript (proposal_decided). */
  states: ReadonlyMap<string, ProposalDecidedPayload>
  /** Absent: the card cannot read the proposal (it says it waits for the owner). Rejects with
   *  { status: 404 } for a person who is not the owner. */
  load?: (id: string) => Promise<ProposalDetail>
  /** Absent: the card is read-only. Rejects with { status } and the server's message. */
  decide?: (id: string, action: 'approve' | 'reject', d?: ProposalDecision) => Promise<ProposalDetail>
  /** Opens the host's draft editor for a board card (approve from there). Absent: no Edit. */
  onEdit?: (id: string) => void
  /** Opens a created card in the host's board. Absent: the card has no Open. */
  onOpenCard?: (card: FiledCard) => void
  /** Opens the session's permissions (where a permission is revoked). */
  onManagePermissions?: () => void
  /** Bumps when the host learns a proposal changed outside the card (its editor approved it). */
  revision?: number
}

export const ProposalContext = createContext<ProposalContextValue | null>(null)

export function useProposals(): ProposalContextValue | null {
  return useContext(ProposalContext)
}
