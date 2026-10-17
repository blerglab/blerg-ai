// What the pieces of a chat share without props: the session, its transport (and the file
// operations when the transport has them), and the file actions the transcript's cards need —
// open a file in the viewer, which version is the newest, which uploaded file a name means.
import type { PatchSummary } from '../model/patch'
import { createContext, useContext } from 'react'
import type { ReviewFile } from '../model/review'
import type { ArtifactInfo, TransportFiles } from '../transport/types'
import type { ArtifactPayload } from '../types'
import type { ArtifactLink } from '../model/artifactLinks'

export interface ChatContextValue {
  sessionId: string
  /** Absent when the transport has no file operations: cards then show no actions. */
  files: TransportFiles | null
  /** Opens the file in the viewer; autoplay starts a video or audio file at once. */
  view: (a: ArtifactInfo, opts?: { autoplay?: boolean }) => void
  artifactLink?: (href: string | undefined) => ArtifactLink | null
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
  /** The session is over (stopped, ended or failed): a run with no end recorded is not running. */
  sessionOver?: boolean
  /** Opens a file the agent's text names, from the session's folder; absent: such text stays text. */
  openWorkspaceFile?: (path: string, line?: number) => void
  /** Opens the diff a checklist item names (its artifact id), at a commit's section when one is
   *  given, when the session's file list has it; null when it does not (the item then shows no
   *  "View diff", and its sha chips copy). */
  viewDiff?: (artifactId: string) => ((commit?: string) => void) | null
  /** A diff's files and +/− counts (read once, cached), for "View diff". */
  diffSummary?: (artifactId: string) => Promise<PatchSummary>
}

export const ChatContext = createContext<ChatContextValue | null>(null)

export function useChat(): ChatContextValue | null {
  return useContext(ChatContext)
}
