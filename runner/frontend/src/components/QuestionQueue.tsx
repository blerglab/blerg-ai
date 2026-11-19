import { Fragment, useContext, useState, type ReactNode } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { AskReply, useTicker } from '@blerglab/chat'
import { useQuestionItems, type AttentionItem } from '../hooks/useQuestionItems'
import AttentionRow from './AttentionRow'
import BlackboardProposal from './BlackboardProposal'
import ReviewRequestRow from './ReviewRequestRow'
import { OpenReviewContext } from '../lib/openReview'
import { dismissWithUndo } from '../lib/needsYouDismiss'
import { AttentionDismiss } from './AttentionRow'
import { useSessionStore } from '../hooks/useSessionStore'
import { apiFetch } from '../apiFetch'
import QuestionRow from './QuestionRow'
import RepositoryRequest from './RepositoryRequest'
import { questionWaitText, waitLabel } from '../lib/sessionRail'

export default function QuestionQueue({ sessionId }: { sessionId: string }) {
  const { root, items } = useQuestionItems(sessionId)
  return <section className="question-queue" aria-label="Question queue" style={{ padding: 8, minWidth: 0 }}><AttentionRows root={root} items={items} /></section>
}

export function AttentionRows({ root, items, onOpenArtifact, renderAsk, renderDecision, inlineAnswers = false, active = true }: { root: string; items: AttentionItem[]; inlineAnswers?: boolean; active?: boolean; onOpenArtifact?: (owner: string, artifact: string) => void; renderAsk?: (question: AttentionItem & {kind: 'ask'}) => ReactNode; renderDecision?: (item: AttentionItem & {kind: 'decision'}) => ReactNode }) {
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  const sessions = useSessionStore(s => s.sessions)
  const questions = items.filter(item => item.kind === 'ask')
  const reviews = items.flatMap(item => item.kind === 'review' ? [{ sessionId: item.session.id, review: item.review }] : [])
  const repositories = items.filter(item => item.kind === 'repository').map(item => item.session.id)
  const openReview = useContext(OpenReviewContext)
  const navigate = useNavigate()
  const now = useTicker(questions.length > 0)
  return <ul className="attention-list rail-list">
    {items.length === 0 && <li className="rail-empty">No questions waiting for an answer.</li>}
    {items.filter(item => !['ask', 'review', 'repository'].includes(item.kind)).map(item => <li className="rail-item rail-ask" key={item.id}>{item.kind === 'decision' && renderDecision?.(item) || <OtherAttentionRow item={item} onOpenArtifact={onOpenArtifact} />}</li>)}
    {repositories.map(id => <li className="rail-item rail-ask" key={id}><RepositoryRequest sessionId={id} /></li>)}
    {reviews.map(({ sessionId: owner, review }) => <li className="rail-item rail-ask" data-testid="rail-review-request" key={review.request_id}><ReviewRequestRow review={review} onOpen={() => { if (onOpenArtifact) onOpenArtifact(owner, review.artifact_id); else if (openReview) openReview(owner, review.artifact_id); else navigate(`/sessions/${encodeURIComponent(owner)}?artifact=${encodeURIComponent(review.artifact_id)}`) }} onDismiss={() => dismissWithUndo(`review:${review.request_id}`, `Review: ${review.artifact_name}`)} /></li>)}
    {questions.map(item => { const question = item.question; const custom = renderAsk?.(item); return custom ? <Fragment key={item.id}>{custom}</Fragment> : <li className="rail-item rail-ask" key={item.id}><QuestionRow active={active} inlineAnswers={inlineAnswers} statusAction={inlineAnswers ? <Link className="question-thread-action" aria-label="Open conversation" title="Open conversation" to={`/sessions/${encodeURIComponent(question.session_id)}`}>↗</Link> : undefined} headerActions={!(question.owner_only && !question.no_wait) ? <AttentionDismiss onDismiss={() => dismissWithUndo(`ask:${question.id}`, question.body)} /> : undefined} sessionId={question.session_id} decisionButtons={question.decision === true} audience="you" wait={Number.isFinite(Date.parse(question.created_at)) ? waitLabel(Date.parse(question.created_at), now) : undefined} key={question.id} id={question.id} question={question.body} worker={question.session_id === root ? 'Main' : sessions.find(s => s.id === question.session_id)?.title}>
      {!inlineAnswers && <p className="question-card-state">{questionWaitText(question.no_wait, question.waiter_ended)}{Number.isFinite(Date.parse(question.created_at)) && <> · {waitLabel(Date.parse(question.created_at), now)}</>}</p>}
      <AskReply compact={inlineAnswers} binary={question.decision === true} draft={drafts[question.id] ?? ''} onDraft={text => setDrafts(d => ({ ...d, [question.id]: text }))} messageId={question.id} answer={async (answer, id) => {
        try {
          const response = await apiFetch(`/api/messages/${encodeURIComponent(id!)}/reply`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ answer }) })
          if (!response.ok) console.warn('Question reply rejected', { status: response.status })
          return response.ok
        } catch (cause) { console.warn('Question reply failed', { cause: cause instanceof Error ? cause.name : 'unknown' }); return false }
      }} />
      {!inlineAnswers && <Link className="rail-link" to={`/sessions/${encodeURIComponent(question.session_id)}`}>Open conversation</Link>}
    </QuestionRow></li> })}
  </ul>
}

function OtherAttentionRow({ item, onOpenArtifact }: { item: AttentionItem; onOpenArtifact?: (owner: string, artifact: string) => void }) {
  const [reviewing, setReviewing] = useState(false)
  const navigate = useNavigate()
  const openReview = useContext(OpenReviewContext)
  const title = item.kind === 'plan' ? `Review plan · ${item.plan.proposal.changes.length} ${item.plan.proposal.changes.length === 1 ? 'change' : 'changes'}` : item.kind === 'failed' ? `${item.session.title || 'Worker'} · Failed` : item.kind === 'paused' ? `${item.session.title || 'Worker'} · Paused` : item.kind === 'decision' ? item.title : `${item.session.title || 'Chat'} · Waiting`
  return <><AttentionRow aria-label={title} onClick={() => {
    if (item.kind === 'plan') setReviewing(true)
    else if (item.kind === 'decision' && item.artifactId && onOpenArtifact) onOpenArtifact(item.session.id, item.artifactId)
    else if (item.kind === 'decision' && item.artifactId && openReview) openReview(item.session.id, item.artifactId)
    else navigate(item.kind === 'decision' && !item.id.startsWith('delegation:') ? `/proposals?session=${encodeURIComponent(item.session.id)}` : `/sessions/${encodeURIComponent(item.session.id)}`)
  }}><span className="question-card-glyph" aria-hidden="true">{item.kind === 'failed' ? '!' : item.kind === 'paused' ? 'Ⅱ' : '◇'}</span><span className="question-card-summary"><span className="question-card-title">{title}</span></span></AttentionRow>
    {reviewing && item.kind === 'plan' && <BlackboardProposal proposal={item.plan.proposal} milestoneName={item.plan.milestoneName} title={() => undefined} onClose={() => setReviewing(false)} />}
  </>
}
