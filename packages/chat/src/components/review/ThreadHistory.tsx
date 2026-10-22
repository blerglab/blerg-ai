import {reviewReplyHistory, type ReviewCommentAction, type ReviewRequest} from '../../model/review'
import Markdown from '../Markdown'
import {TimeLabel} from '../TimeLabel'

const PERSON_WORD: Record<ReviewCommentAction, string> = {reply: 'replied', reopen: 'reopened', resolve: 'resolved', reject: 'rejected'}

/** Everything said on a thread after its request, oldest first: the agent's answers and the person's
 *  replies, resolves, rejects and reopens, each with who and when. */
export default function ThreadHistory({request}: {request: ReviewRequest}) {
  const entries = [
    ...reviewReplyHistory(request).map(reply => ({text: reply.text, createdAt: reply.timeUnknown ? '' : reply.createdAt, revision: reply.revision ?? 0, person: false,
      label: `Agent · ${reply.status === 'declined' ? 'Rejected' : 'Resolved'}`})),
    ...(request.agentReopens ?? []).map(reopen => ({text: reopen.text || '_(The reason could not be shown.)_', createdAt: reopen.createdAt, revision: reopen.revision, person: false,
      label: 'Reopened by the agent'})),
    ...(request.comments ?? []).map(comment => ({text: comment.text, createdAt: comment.createdAt, revision: comment.revision, person: true,
      label: `Person · ${PERSON_WORD[comment.action ?? 'reopen']}`})),
  ].sort((a, b) => a.revision - b.revision || Number(b.person) - Number(a.person) || (Date.parse(a.createdAt) || 0) - (Date.parse(b.createdAt) || 0))
  return <>{entries.map((entry, index) => <div className={`review-reply${entry.person ? ' is-person' : ''}`} key={index} data-testid="thread-entry">
    <span className="review-where">{entry.label}{entry.createdAt && <> <TimeLabel ts={entry.createdAt} /></>}</span>
    {entry.text.trim() && <Markdown text={entry.text}/>}
  </div>)}</>
}
