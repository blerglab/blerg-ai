import { Hover, Markdown } from '@blerglab/chat'
import AttentionRow, { AttentionDismiss } from './AttentionRow'
import type { LiveReviewRequest } from '../lib/liveState'

export default function ReviewRequestRow({ review, onOpen, onDismiss }: { review: LiveReviewRequest; onOpen(): void; onDismiss(): void }) {
  const title = `Review: ${review.artifact_name}`
  return <Hover target={{ kind: 'card', source: 'review', key: review.request_id, title }} content={<div className="question-preview">
    <div>{review.artifact_name} · v{review.artifact_version}</div>
    <div className="question-preview-text"><Markdown text={review.ask} /></div>
    <div className="question-preview-footer">Click to review this file</div>
  </div>}><AttentionRow onClick={onOpen} aria-label={title} action={<AttentionDismiss label={`Dismiss review request: ${review.artifact_name}`} onDismiss={onDismiss} />}>
    <span className="question-card-glyph" aria-hidden="true">✎</span><span className="question-card-summary"><span className="question-card-title">{title}</span></span>
  </AttentionRow></Hover>
}
