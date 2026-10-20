// What a review request's card and the viewer's review bar need from their chat: the requests seen
// (the transcript's, merged with the server's list), whether this person may decide them, and the
// way to decide. Apart from ChatContext so a decision redraws the review pieces only.
import { createContext, useContext } from 'react'
import type { ReviewRequestPayload } from '../types'

export type ReviewDecision = 'approve' | 'request_changes'

export interface ReviewRequestContextValue {
  /** The request that applies to this file version, or null. */
  forArtifact: (artifactId: string) => ReviewRequestPayload | null
  /** Absent: this person cannot decide (not the session's owner, or a read-only chat). Rejects with
   *  { status } and the server's message. */
  decide?: (requestId: string, decision: ReviewDecision, note?: string) => Promise<ReviewRequestPayload>
  /** Opens the file at the request's version in the viewer; absent where the chat cannot. */
  open?: (r: ReviewRequestPayload) => void
}

export const ReviewRequestContext = createContext<ReviewRequestContextValue | null>(null)

export function useReviewRequests(): ReviewRequestContextValue | null {
  return useContext(ReviewRequestContext)
}
