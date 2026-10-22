import { PROPOSAL_CAP_REASON, actorLabel, proposalExpiring, proposalExtension } from '../lib/proposalAttention'
import { useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTicker } from '@blerglab/chat'
import { apiFetch } from '../apiFetch'
import { proposalsChanged } from '../hooks/useProposalInbox'
import { approveFailureMessage } from '../lib/proposals'
import type { ProposalInfo } from '../types'
import ProposalPreview from './ProposalPreview'
import BoardCreateAccess from './BoardCreateAccess'
import { requestedBoardAccess } from '../lib/boardCreate'
import type { BoardLevel } from '../lib/permissionsModal'

async function responseError(res: Response) {
  const body = await res.json().catch(() => ({})) as { error?: string }
  return body.error || `HTTP ${res.status}`
}
export function ProposalExpiry({ proposal: p, onChanged }: { proposal: ProposalInfo; onChanged?: () => void }) {
  const now = useTicker(p.state === 'pending')
  const [busy, setBusy] = useState(false)
  const busyRef = useRef(false)
  const [error, setError] = useState('')
  if (p.state !== 'pending') return null
  const soon = proposalExpiring(p, now)
  const expired = Date.parse(p.expires_at) <= now
  const extension = proposalExtension(p)
  async function extend() {
    if (busyRef.current) return
    busyRef.current = true; setBusy(true); setError('')
    try {
      const res = await apiFetch(`/api/proposals/${encodeURIComponent(p.id)}/extend`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ expected_expires_at: p.expires_at }) })
      if (!res.ok) throw new Error(await responseError(res))
      proposalsChanged(); onChanged?.()
    } catch (e) { setError(e instanceof Error ? e.message : 'Could not extend the proposal.') }
    finally { busyRef.current = false; setBusy(false) }
  }
  return <div className={`proposal-expiry${soon ? ' soon' : ''}`}>
    {expired ? <span role="alert">Expired — this request cannot be approved or extended.</span> : <>
      <span role={soon ? 'alert' : undefined}>{soon ? 'Expires soon — ' : 'Expires '}<time dateTime={p.expires_at}>{new Date(p.expires_at).toLocaleString()}</time></span>
      {soon && (extension.allowed
        ? <button type="button" disabled={busy} onClick={() => void extend()}>{busy ? 'Extending…' : extension.label}</button>
        : <><button type="button" disabled aria-describedby={`proposal-cap-${p.id}`}>{extension.label}</button><span id={`proposal-cap-${p.id}`} className="proposal-cap">{PROPOSAL_CAP_REASON}</span></>)}
    </>}
    {(p.extensions?.length ?? 0) > 0 && <span className="proposal-extended">Extended {p.extensions!.length === 1 ? 'once' : `${p.extensions!.length} times`} by {actorLabel(p.extensions![p.extensions!.length - 1].by_you)}</span>}
    {error && <span role="alert">{error}</span>}
  </div>
}

export function PendingProposal({ proposal: p, refresh }: { proposal: ProposalInfo; refresh: () => void }) {
  const [confirm, setConfirm] = useState(false)
  const [busy, setBusy] = useState(false)
  const inFlight = useRef(false)
  const [error, setError] = useState('')
  const boardCreate = p.kind === 'board_create'
  const [access, setAccess] = useState<BoardLevel>(() => requestedBoardAccess(p))
  const exact = p.kind === 'board_file' || p.kind === 'plugin_request' || boardCreate
  async function decide(action: 'approve' | 'reject') {
    if (inFlight.current) return
    inFlight.current = true; setBusy(true); setError('')
    const body = action === 'approve' && exact
      ? { expected_arguments_raw: p.arguments_raw, ...(p.kind === 'plugin_request' ? { scope: p.plugin_session_apply ? 'session' : 'always' } : {}),
          ...(boardCreate && access !== requestedBoardAccess(p) ? { access } : {}) } : {}
    try {
      const res = await apiFetch(`/api/proposals/${encodeURIComponent(p.id)}/${action}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
      if (!res.ok) throw new Error(action === 'approve' ? approveFailureMessage(res.status, await responseError(res)) : await responseError(res))
      proposalsChanged(); refresh()
    } catch (e) { setError(e instanceof Error ? e.message : 'Could not decide the proposal.'); refresh() }
    finally { inFlight.current = false; setBusy(false); setConfirm(false) }
  }
  return <div className="pending-proposal" data-testid={`pending-proposal-${p.id}`}>
    <ProposalPreview proposal={p} complete={confirm} />
    <ProposalExpiry proposal={p} onChanged={refresh} />
    <div className="proposal-attention-actions">
      {confirm ? <>
        {boardCreate && <BoardCreateAccess proposal={p} value={access} onChange={setAccess} disabled={busy} />}
        <p>{boardCreate ? 'Create this board with your access?' : <>Approve this exact request{p.kind === 'plugin_request' ? p.plugin_session_apply ? ' for this session only' : ' for new sessions' : ''}?</>}</p>
        <button type="button" disabled={busy} onClick={() => void decide('approve')}>Confirm approve</button>
        <button type="button" disabled={busy} onClick={() => setConfirm(false)}>Cancel</button>
      </> : <>
        <button type="button" disabled={busy || exact && !p.arguments_raw} onClick={() => setConfirm(true)}>Approve</button>
        <button type="button" disabled={busy} onClick={() => void decide('reject')}>Reject</button>
        <Link to={`/proposals?id=${encodeURIComponent(p.id)}`}>Review{p.kind === 'board_file' ? ' or edit' : ''}</Link>
      </>}
      {p.session_id && <Link to={`/sessions/${encodeURIComponent(p.session_id)}`}>Open session</Link>}
    </div>
    {error && <p role="alert">{error}</p>}
  </div>
}
