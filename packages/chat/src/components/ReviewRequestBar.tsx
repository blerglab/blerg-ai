// The bar over a file the session asked its owner to review (an explicit review request at this exact
// version): the ask, Approve and Request changes (with an optional note; the comments already
// submitted on this version go with it). Shown only for an explicit request; once decided it says
// the outcome. Anyone who is not the owner sees that it waits for the owner.
import { useState } from 'react'
import type { ArtifactInfo } from '../transport/types'
import type { ReviewRequestPayload } from '../types'
import { reviewOutcomeLine } from '../model/reviewRequests'
import { useReviewRequests, type ReviewDecision } from './reviewRequestContext'

export default function ReviewRequestBar({ artifact }: { artifact: ArtifactInfo }) {
  const ctx = useReviewRequests()
  const fromCtx = ctx?.forArtifact(artifact.id) ?? null
  const [local, setLocal] = useState<ReviewRequestPayload | null>(null)
  const [noting, setNoting] = useState(false)
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const r = local && (!fromCtx || fromCtx.request_id === local.request_id && fromCtx.state === 'pending') ? local : fromCtx
  if (!r) return null
  const decide = ctx?.decide
  const pending = r.state === 'pending'

  async function send(decision: ReviewDecision) {
    if (!decide || busy || !r) return
    setBusy(true)
    setError('')
    try {
      setLocal(await decide(r.request_id, decision, note.trim() || undefined))
      setNoting(false)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Could not send the decision.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className={`review-request-bar status-${r.state}`} data-testid="review-request-bar" aria-label="Review request">
      <div className="review-request-bar-text">
        <span className="review-request-bar-label">{pending ? 'Review requested' : reviewOutcomeLine(r)}</span>
        {r.ask && <span className="review-request-bar-ask" data-testid="review-request-bar-ask">{r.ask}</span>}
        {!pending && r.note && r.state !== 'withdrawn' && <span className="review-request-bar-note">{r.note}</span>}
      </div>
      {pending && decide && !noting && (
        <div className="review-request-bar-actions">
          <button type="button" className="delegation-btn primary" disabled={busy} onClick={() => void send('approve')}>Approve</button>
          <button type="button" className="delegation-btn" disabled={busy} onClick={() => setNoting(true)}>Request changes</button>
        </div>
      )}
      {pending && decide && noting && (
        <form className="review-request-bar-form" onSubmit={e => { e.preventDefault(); void send('request_changes') }}>
          <textarea
            className="review-request-bar-input"
            aria-label="What should change (optional)"
            placeholder="What should change (optional). Comments you submitted on this version are included."
            value={note}
            maxLength={4000}
            rows={2}
            autoFocus
            onChange={e => setNote(e.target.value)}
          />
          <div className="review-request-bar-actions">
            <button type="submit" className="delegation-btn primary" disabled={busy}>Send</button>
            <button type="button" className="delegation-btn" disabled={busy} onClick={() => setNoting(false)}>Cancel</button>
          </div>
        </form>
      )}
      {pending && !decide && <span className="review-request-bar-wait">Waiting for the session owner's decision.</span>}
      {error && <div className="delegation-error" role="alert" data-testid="review-request-bar-error">{error}</div>}
    </section>
  )
}
