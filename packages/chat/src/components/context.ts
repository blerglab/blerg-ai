// What the pieces of a chat share without props: the session, its transport (and the file
// operations when the transport has them), and the file actions the transcript's cards need —
// open a file in the viewer, which version is the newest, which uploaded file a name means.
import type { PatchSummary } from '../model/patch'
import { createContext, useContext } from 'react'
import type { ReviewFile } from '../model/review'
import type { ArtifactInfo, TransportFiles } from '../transport/types'
import type { ArtifactPayload, ChecklistItem } from '../types'
import type { ArtifactLink } from '../model/artifactLinks'

export interface ChatContextValue {
  sessionId: string
  checklistItems?: readonly ChecklistItem[]
  onOpenChecklistItem?: (itemId: string) => void
  /** Absent when the transport has no file operations: cards then show no actions. */
  files: TransportFiles | null
  /** Opens the file in the viewer; autoplay starts a video or audio file at once. */
  view: (a: ArtifactInfo, opts?: { autoplay?: boolean }) => void
  artifactLink?: (href: string | undefined) => ArtifactLink | null
  /** The newest version number of the file a card's payload is a version of (from the list). */
  latestVersion: (a: ArtifactPayload) => number | undefined
  /** Resolves an attachment by its stable identity, including earlier same-name uploads. */
  attachmentById?: (id: string) => ArtifactInfo | undefined
  /** For a message sent before attachment IDs: the newest upload of that name made at or before
   *  the message's time `at` (never a later one); undefined without a time. */
  attachmentAt?: (name: string, at: string | undefined) => ArtifactInfo | undefined
  /** Legacy name lookup for host cards; never used for a message attachment preview. */
  byName: (name: string) => ArtifactInfo | undefined
  /** Sends a message with files attached the ordinary way (uploads first, then the message with
   *  the attachment note). false when nothing was sent. */
  submitFeedback: (text: string, files: File[]) => Promise<boolean>
  /** The newest review file for `name` of either origin, merged (person's base, agent's replies).
   *  null when none. */
  reviewFor: (name: string) => Promise<ReviewFile | null>
  /** Opens the review of a file the session published: the reviewed version when the list has it
   *  (by artifact id), else the newest; absent where the chat cannot open files. */
  openReview?: (name: string, artifactId?: string) => void
  /** The session is over (stopped, ended or failed): a run with no end recorded is not running. */
  sessionOver?: boolean
  /** The session's configured effort: a subagent that reports none of its own runs at it. */
  sessionEffort?: string
  /** Opens a file the agent's text names, from the session's folder; absent: such text stays text. */
  /** `folder`: the path names the folder's file even when a published file has the same name (a
   *  relative link inside a file read from the folder). */
  openWorkspaceFile?: (path: string, line?: number, opts?: { folder?: boolean }) => void
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
