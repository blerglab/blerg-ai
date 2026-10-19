import {reviewReplyHistory, type ReviewRequest} from '../../model/review'
import Markdown from '../Markdown'

export default function ThreadHistory({request}: {request: ReviewRequest}) {
  const entries = [
    ...reviewReplyHistory(request).map(reply => ({...reply, person: false})),
    ...(request.comments ?? []).map(comment => ({...comment, person: true, status: 'open' as const})),
  ].sort((a,b) => (a.revision ?? 0) - (b.revision ?? 0) || Number(b.person) - Number(a.person))
  return <>{entries.map((entry, index) => <div className="review-reply" key={index}>
    <span className="review-where">{entry.person ? 'Person · reopened' : `Agent · ${entry.status === 'declined' ? 'Rejected' : 'Resolved'}`}</span>
    <Markdown text={entry.text}/>
  </div>)}</>
}
