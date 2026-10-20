// The session's explicit request that its owner review one file or diff, in the chat: one compact
// card, "Review requested: SECURITY.md · v2 · Open", with the ask under it. Open shows the file at
// that version, where the viewer's review bar has Approve and Request changes. Decided, the card says
// how (in the delegation and proposal cards' family).
import type { AgentEvent } from '../../types'
import { readReviewRequest, reviewOutcomeLine } from '../../model/reviewRequests'
import { useReviewRequests } from '../reviewRequestContext'
import { TimeLabel } from '../TimeLabel'

export default function ReviewRequestCard({ ev }: { ev: AgentEvent }) {
  const ctx = useReviewRequests()
  const asked = readReviewRequest(ev.payload)
  if (!asked) return null
  const current = ctx?.forArtifact(asked.artifact_id)
  const r = current && current.request_id === asked.request_id ? current : asked
  const line = reviewOutcomeLine(r)
  return (
    <div className={`agent-card delegation-card review-request-card status-${r.state}`} data-testid="review-request-card" data-event-id={ev.client_event_id} data-request-id={r.request_id}>
      <div className="delegation-head">
        <span className="delegation-title">Review requested: <b>{r.artifact_name}</b> · v{r.artifact_version}</span>
        {ctx?.open && <button type="button" className="delegation-btn review-request-open" onClick={() => ctx.open?.(r)}>Open</button>}
        <TimeLabel ts={ev.ts} className="card-time-end" />
      </div>
      {r.ask && <div className="delegation-prompt review-request-ask" data-testid="review-request-ask">{r.ask}</div>}
      {r.state === 'pending' && !ctx?.decide && <div className="delegation-readonly">Waiting for the session owner's review.</div>}
      {line && (
        <div className="delegation-decided" data-testid="review-request-outcome">
          <span>{line}</span>
          {r.note && r.state !== 'withdrawn' && <span className="review-request-note">{r.note}</span>}
        </div>
      )}
    </div>
  )
}
