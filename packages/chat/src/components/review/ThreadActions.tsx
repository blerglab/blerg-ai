// What the person can do on a review thread, like a GitHub reviewer: reply to it, and resolve or
// reject it themselves (an open thread), besides Reopen (a settled one). Each is queued with the
// review and sent on Submit.
import {useState} from 'react'
import type {ReviewRequest} from '../../model/review'
import CommentEditor from './CommentEditor'
import {shortLabel} from './DiffReview'

export default function ThreadActions({request, busy, onReply, onSettle}: {
  request: ReviewRequest
  busy?: boolean
  onReply(text: string): void
  onSettle(status: 'done' | 'declined'): void
}) {
  const [replying, setReplying] = useState(false)
  const [text, setText] = useState('')
  const limit = (request.revision ?? 0) >= 1_000_000
  const disabled = busy || limit
  if (replying) {
    const words = text.trim()
    return <form className="review-form review-thread-reply" aria-label={`Reply to: ${shortLabel(request.text)}`} onSubmit={event => {
      event.preventDefault()
      if (disabled || !words || words.length > 32768) return
      onReply(words)
      setText(''); setReplying(false)
    }}>
      <CommentEditor label="Reply" value={text} onChange={setText} autoFocus/>
      <div className="review-form-actions">
        <button type="submit" className="artifact-btn" disabled={disabled || !words || words.length > 32768}>Reply</button>
        <button type="button" className="artifact-btn" disabled={busy} onClick={() => {setText(''); setReplying(false)}}>Cancel</button>
      </div>
    </form>
  }
  return <span className="review-thread-actions">
    <button type="button" className="artifact-btn" disabled={disabled} aria-label={`Reply to: ${shortLabel(request.text)}`} onClick={() => setReplying(true)}>Reply</button>
    {request.status === 'open' && <>
      <button type="button" className="artifact-btn" disabled={disabled} aria-label={`Resolve: ${shortLabel(request.text)}`} onClick={() => onSettle('done')}>Resolve</button>
      <button type="button" className="artifact-btn" disabled={disabled} aria-label={`Reject: ${shortLabel(request.text)}`} onClick={() => onSettle('declined')}>Reject</button>
    </>}
  </span>
}
