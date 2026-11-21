import { useState } from 'react'
import { apiFetch } from '../apiFetch'
import { useSessionStore } from '../hooks/useSessionStore'
import type { SessionInfo } from '../types'
import QuestionRow from './QuestionRow'
import './DockRepositories.css'

export type RepositoryApproval = NonNullable<SessionInfo['repository_request']>
function repositoryQuestion(request: RepositoryApproval) {
  return request.kind === 'add' ? `Add ${request.member_name}?` : `Remove ${request.member_name}?`
}

/** All entry points decide the exact journal operation; no separate approval is created. */
export function RepositoryDecision({ sessionId, request }: { sessionId: string; request: RepositoryApproval }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function decide(allow: boolean) {
    setBusy(true)
    setError('')
    try {
      const response = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/workspace/repositories/${encodeURIComponent(request.id)}/decide`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ allow, proposed_digest: request.proposed_digest }),
      })
      if (!response.ok) throw new Error((await response.json()).error || 'Could not send the decision. Try again.')
      useSessionStore.setState(state => ({ sessions: state.sessions.map(session => session.id === sessionId && session.repository_request?.id === request.id ? { ...session, repository_request: undefined } : session) }))
      window.dispatchEvent(new Event('blerg:repository-requests-changed'))
    } catch (error) { setError(error instanceof Error ? error.message : 'Could not send the decision. Try again.') }
    finally { setBusy(false) }
  }
  if (request.refusal) return <p role="alert">{request.refusal}</p>
  return <>
    <p>The workspace will change when it is safe. Work in progress keeps running.</p>
    <div className="repository-decide">
      <button type="button" className="btn small" disabled={busy} onClick={() => void decide(false)}>Decline</button>
      <button type="button" className="btn primary small" disabled={busy} onClick={() => void decide(true)}>Approve</button>
    </div>
    {error && <p role="alert">{error}</p>}
  </>
}

export default function RepositoryRequest({ sessionId, card = false }: { sessionId: string; card?: boolean }) {
  const request = useSessionStore(s => s.sessions.find(session => session.id === sessionId)?.repository_request)
  if (!request || request.refusal) return null
  if (card) return <div className="agent-card delegation-card proposal-card status-pending" role="group" aria-label="Repository approval"><strong>{repositoryQuestion(request)}</strong><RepositoryDecision key={request.id} sessionId={sessionId} request={request} /></div>
  return <QuestionRow sessionId={sessionId} approval id={request.id} audience={request.refusal ? undefined : 'you'} question={repositoryQuestion(request)}><RepositoryDecision key={request.id} sessionId={sessionId} request={request} /></QuestionRow>
}
