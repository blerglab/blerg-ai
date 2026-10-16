// What an ask card needs from its chat: whether its ask is still open, and a way to answer it.
// Kept apart from ChatContext so a change to the asks redraws the ask cards only.
import { createContext, useContext } from 'react'
import type { AskState } from '../model/asks'

export interface AskContextValue {
  /** By the ask event's client_event_id. */
  states: ReadonlyMap<string, AskState>
  /** Sends the answer to the ask (messageId: its messages API id, when it has one); false when
   *  nothing was sent. Absent: the transport cannot address an answer, the card has no reply box. */
  answer?: (text: string, messageId?: string) => Promise<boolean>
}

export const AskContext = createContext<AskContextValue | null>(null)

export function useAsks(): AskContextValue | null {
  return useContext(AskContext)
}
