// What a delegation card needs from its chat: the decisions seen so far and the host's ways to
// decide, read the whole prompt and open the child. Apart from ChatContext so a decision redraws
// the delegation cards only.
import { createContext, useContext } from 'react'
import type { DelegationState } from '../model/delegations'
import type { DelegationChoice, DelegationDecidedPayload } from '../types'

export interface DelegationContextValue {
  /** By request_id. */
  states: ReadonlyMap<string, DelegationState>
  /** Absent: the transport cannot decide, the card is read-only. */
  decide?: (requestId: string, choice: DelegationChoice) => Promise<DelegationDecidedPayload>
  /** Absent: no "Show all". */
  fullPrompt?: (requestId: string) => Promise<string>
  /** Where the person decides when this chat cannot (the runner's session page), or null. */
  decideUrl?: string | null
  /** Opens a child session in the app; absent: the card names the child's short id. */
  /** Opens the current parent session's child approval settings. */
  onManageChildApproval?: () => void
  onOpenSession?: (sessionId: string) => void
}

export const DelegationContext = createContext<DelegationContextValue | null>(null)

export function useDelegations(): DelegationContextValue | null {
  return useContext(DelegationContext)
}
