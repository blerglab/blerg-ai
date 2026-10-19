import {useState} from 'react'
import type {ReviewRequest} from '../../model/review'
import CommentEditor from './CommentEditor'

export default function ReopenThread({request, busy, onReopen}: {
  request: ReviewRequest
  busy?: boolean
  onReopen(explanation: string): void
}) {
  const [editing, setEditing] = useState(false)
  const [text, setText] = useState('')
  const disabled = busy || (request.revision ?? 0) >= 1_000_000
  if (!editing) return <button type="button" className="artifact-btn" disabled={disabled} onClick={() => setEditing(true)}>Reopen</button>
  return <form className="review-form" onSubmit={event => {
    event.preventDefault()
    if (disabled || !text.trim() || text.trim().length > 32768) return
    onReopen(text.trim())
    setText(''); setEditing(false)
  }}>
    <CommentEditor label="Reason for reopening" value={text} onChange={setText} autoFocus/>
    <div className="review-form-actions">
      <button type="submit" className="artifact-btn" disabled={disabled || !text.trim() || text.trim().length > 32768}>Reopen with comment</button>
      <button type="button" className="artifact-btn" disabled={busy} onClick={() => {setText(''); setEditing(false)}}>Cancel</button>
    </div>
  </form>
}
