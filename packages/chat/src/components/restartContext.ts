// What a paused restart row needs from its chat: where it stands, and a way to resume the session.
// Kept apart from ChatContext so a change here redraws the restart rows only.
import { createContext, useContext } from 'react'
import type { RestartRowState } from '../model/userMessages'

export interface RestartContextValue {
  /** By the restart row's client_event_id (model/userMessages.ts restartRowStates). */
  states: ReadonlyMap<string, RestartRowState>
  /** Sends the session the resume message for this row; false when nothing was sent. Absent: this
   *  viewer cannot send to the session (read only, ended), the row has no Resume button. */
  resume?: (eventId: string) => Promise<boolean>
}

export const RestartContext = createContext<RestartContextValue | null>(null)

export function useRestartRows(): RestartContextValue | null {
  return useContext(RestartContext)
}
