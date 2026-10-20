// Review an authoritative request outside the paged conversation, using the same decision card.
import { useMemo } from 'react'
import type { AgentEvent, DelegationChoice, DelegationDecidedPayload } from '../types'
import { DelegationContext } from './delegationContext'
import DelegationCard from './cards/DelegationCard'

export function DelegationReview({ event, decision, decide, onOpenSession }: {
  event: AgentEvent
  decision?: DelegationDecidedPayload
  decide: (id: string, choice: DelegationChoice) => Promise<DelegationDecidedPayload>
  onOpenSession?: (id: string) => void
}) {
  const value = useMemo(() => ({
    states: new Map(decision ? [[decision.request_id, { decided: decision, decidedAt: null }]] : []),
    decide, onOpenSession,
    fullPrompt: async (id: string) => {
      const payload = event.payload as { request_id: string; prompt: string }
      if (id !== payload.request_id) throw new Error('Request unavailable.')
      return payload.prompt
    },
  }), [decision, decide, onOpenSession, event])
  return <DelegationContext.Provider value={value}><DelegationCard ev={event} /></DelegationContext.Provider>
}
