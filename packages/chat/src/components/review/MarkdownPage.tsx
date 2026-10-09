// MarkdownPage: the edited source laid out as it reads, or (Changes on) the word diff against the
// published text. The root div is where selections are anchored and highlights drawn: every change
// of the requests, the focus or the text redraws them through applyHighlights.
import { useEffect, useMemo, type RefObject } from 'react'
import Markdown from '../Markdown'
import { applyHighlights } from '../../model/anchor'
import { wordDiff } from '../../model/diff'
import type { ReviewRequest } from '../../model/review'

interface Props {
  source: string
  published: string
  showDiff: boolean
  requests: ReviewRequest[]
  focusedId: string | null
  rootRef: RefObject<HTMLDivElement | null>
}

export default function MarkdownPage({ source, published, showDiff, requests, focusedId, rootRef }: Props) {
  const diff = useMemo(() => (showDiff ? wordDiff(published, source) : null), [showDiff, published, source])

  useEffect(() => {
    const root = rootRef.current
    if (!root || showDiff) return
    return applyHighlights(root, requests, focusedId)
  }, [rootRef, requests, focusedId, source, showDiff])

  return (
    <div className="review-page" data-testid="review-page" ref={rootRef}>
      {diff ? (
        <pre className="review-diff">
          {diff.map((part, i) =>
            part.kind === 'same'
              ? <span key={i}>{part.text}</span>
              : <span key={i} className={part.kind === 'ins' ? 'diff-ins' : 'diff-del'}>{part.text}</span>,
          )}
        </pre>
      ) : (
        <Markdown text={source} className="artifact-markdown" />
      )}
    </div>
  )
}
