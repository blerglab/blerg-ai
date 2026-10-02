import { createContext } from 'react'
import type { ArtifactInfo } from './artifacts'
import type { ArtifactPayload } from '../types'

// What an artifact card in the transcript can do. The chat view provides it; the card (drawn by
// the module-level event renderer, outside the view's own component) reads it.
export interface ArtifactActions {
  sessionId: string
  /** Open the in-app viewer on this file. */
  view: (artifact: ArtifactInfo) => void
  /** The highest version of the file a card is about, from the session's list (undefined until
   *  the list is read), so a version 1 card can say it is no longer the only one. */
  latestVersion?: (artifact: ArtifactPayload) => number | undefined
}

export const ArtifactActionsContext = createContext<ArtifactActions | null>(null)
