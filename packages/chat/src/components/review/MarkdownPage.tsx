// MarkdownPage: the edited source laid out as it reads, or (Changes on) the word diff against the
// published text. The root div is where selections are anchored and highlights drawn: every change
// of the requests, the focus or the text redraws them through applyHighlights.
import { useEffect, useMemo, type RefObject } from 'react'
import Markdown, { type MarkdownDocument } from '../Markdown'
import { applyHighlights } from '../../model/anchor'
import { wordDiff } from '../../model/diff'
import type { ReviewRequest } from '../../model/review'

// A reviewed file is a published one: no folder, so relative images stay placeholders.
const REVIEWED_DOCUMENT: MarkdownDocument = {}

interface Props {
  source: string
  published: string
  showDiff: boolean
  requests: ReviewRequest[]
  focusedId: string | null
  rootRef: RefObject<HTMLDivElement | null>
  /** Whether the rendered page hides words the source holds: an HTML comment or anything else the
   *  sanitizer dropped, a closed <details>, nesting cut short. Review mode then says so. */
  onHiddenContent?: (hidden: boolean) => void
}

/** What on a rendered document hides words of its source (see onHiddenContent). */
export const HIDDEN_CONTENT_SELECTOR = '[data-md-hidden], details:not([open]), .md-too-deep'

export default function MarkdownPage({ source, published, showDiff, requests, focusedId, rootRef, onHiddenContent }: Props) {
  const diff = useMemo(() => (showDiff && source !== published ? wordDiff(published, source) : null), [showDiff, published, source])

  useEffect(() => {
    const root = rootRef.current
    if (!root || showDiff) return
    return applyHighlights(root, requests, focusedId)
  }, [rootRef, requests, focusedId, source, showDiff])

  useEffect(() => {
    const root = rootRef.current
    if (!root || diff || !onHiddenContent) return // the word diff is the source itself: nothing hides
    onHiddenContent(root.querySelector(HIDDEN_CONTENT_SELECTOR) !== null)
  }, [rootRef, source, diff, onHiddenContent])

  return (
    <div className="review-page" data-testid="review-page" ref={rootRef}>
      {diff ? (
        <pre className="review-diff">
          {diff.map((part, i) =>
            part.kind === 'same'
              ? <span key={i}>{part.text}</span>
              : <span key={i} data-review-deletion={part.kind === 'del' ? 'true' : undefined} className={part.kind === 'ins' ? 'diff-ins' : 'diff-del'}>{part.text}</span>,
          )}
        </pre>
      ) : (
        <Markdown text={source} className="artifact-markdown md-doc md-box" document={REVIEWED_DOCUMENT} html />
      )}
    </div>
  )
}
