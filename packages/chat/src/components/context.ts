// What the pieces of a chat share without props: the session, its transport (and the file
// operations when the transport has them), and the file actions the transcript's cards need —
// open a file in the viewer, which version is the newest, which uploaded file a name means.
import { createContext, useContext } from 'react'
import type { ReviewFile } from '../model/review'
import type { ArtifactInfo, TransportFiles } from '../transport/types'
import type { ArtifactPayload } from '../types'

export interface ChatContextValue {
  sessionId: string
  /** Absent when the transport has no file operations: cards then show no actions. */
  files: TransportFiles | null
  /** Opens the file in the viewer; autoplay starts a video or audio file at once. */
  view: (a: ArtifactInfo, opts?: { autoplay?: boolean }) => void
  /** The newest version number of the file a card's payload is a version of (from the list). */
  latestVersion: (a: ArtifactPayload) => number | undefined
  /** The newest version of a file the person attached, by name (for inline previews). */
  byName: (name: string) => ArtifactInfo | undefined
  /** Sends a message with files attached the ordinary way (uploads first, then the message with
   *  the attachment note). false when nothing was sent. */
  submitFeedback: (text: string, files: File[]) => Promise<boolean>
  /** The newest review file for `name` of either origin, merged (person's base, agent's replies).
   *  null when none. */
  reviewFor: (name: string) => Promise<ReviewFile | null>
}

export const ChatContext = createContext<ChatContextValue | null>(null)

export function useChat(): ChatContextValue | null {
  return useContext(ChatContext)
}
