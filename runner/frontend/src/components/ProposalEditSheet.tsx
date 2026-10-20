// ProposalEditSheet: the chat's Edit on a board-card proposal. The Proposals page's draft editor in a
// sheet over the session; saving files nothing, then the person reviews the saved draft and approves
// exactly it from here (optionally allowing this board for the session).
import { useEffect, useId, useState } from 'react'
import { useBackdropClose } from '@blerglab/chat'
import { apiFetch } from '../apiFetch'
import type { ProposalInfo } from '../types'
import BoardFileDraft from './BoardFileDraft'
import './ProposalEditSheet.css'

const text = (v: unknown) => (typeof v === 'string' ? v : '')

export default function ProposalEditSheet({ proposalId, onClose, onDecided }: { proposalId: string; onClose: () => void; onDecided: () => void }) {
  const backdrop = useBackdropClose(onClose)
  const titleId = useId()
  const [proposal, setProposal] = useState<ProposalInfo | null>(null)
  const [step, setStep] = useState<'loading' | 'edit' | 'review' | 'gone'>('loading')
  const [allow, setAllow] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    let live = true
    void apiFetch(`/api/proposals/${encodeURIComponent(proposalId)}`).then(async r => {
      if (!r.ok) throw new Error('load')
      const p = await r.json() as ProposalInfo
      if (!live) return
      setProposal(p)
      setStep(p.kind === 'board_file' && p.state === 'pending' && typeof p.arguments_raw === 'string' ? 'edit' : 'gone')
    }).catch(() => { if (live) setStep('gone') })
    return () => { live = false }
  }, [proposalId])
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  async function approve() {
    if (!proposal?.arguments_raw || busy) return
    setBusy(true); setError('')
    try {
      const r = await apiFetch(`/api/proposals/${encodeURIComponent(proposal.id)}/approve`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ expected_arguments_raw: proposal.arguments_raw, ...(allow ? { allow_for_session: true } : {}) }),
      })
      if (!r.ok) {
        const body = await r.json().catch(() => ({})) as { error?: unknown }
        throw new Error(typeof body.error === 'string' && body.error ? body.error : `HTTP ${r.status}`)
      }
      onDecided()
      onClose()
    } catch (e) { setError(e instanceof Error ? e.message : 'Could not approve the draft.') }
    finally { setBusy(false) }
  }

  const args = proposal?.arguments ?? {}
  return (
    <div className="launch-overlay proposal-sheet-overlay" {...backdrop}>
      <div className="launch-panel proposal-sheet" role="dialog" aria-modal="true" aria-labelledby={titleId} data-testid="proposal-edit-sheet">
        <div className="proposal-sheet-head">
          <h2 id={titleId}>Edit the card draft</h2>
          <button type="button" className="proposal-sheet-close" aria-label="Close" onClick={onClose}>✕</button>
        </div>
        {step === 'loading' && <p role="status">Loading the draft…</p>}
        {step === 'gone' && <p role="status">This proposal is no longer waiting for a decision.</p>}
        {step === 'edit' && proposal && (
          <BoardFileDraft proposal={proposal} onSaved={updated => { setProposal(updated); setStep('review') }} onCancel={onClose} />
        )}
        {step === 'review' && proposal && (
          <div className="proposal-sheet-review" data-testid="proposal-edit-review">
            <p>Saved. Review the draft, then approve it to create the card.</p>
            <dl>
              <dt>Board</dt><dd>{text(args.board_name) || text(args.board_id)}</dd>
              <dt>Title</dt><dd>{text(args.title)}</dd>
              {text(args.type) && <><dt>Type</dt><dd>{text(args.type)}</dd></>}
              <dt>Details</dt><dd className="proposal-sheet-body">{text(args.body)}</dd>
            </dl>
            <label className="rail-check"><input type="checkbox" checked={allow} onChange={e => setAllow(e.target.checked)} /> <span>Also file later cards on this board without asking (this session only)</span></label>
            <div className="proposal-sheet-actions">
              <button type="button" className="proposal-sheet-primary" disabled={busy} onClick={() => void approve()}>{busy ? 'Approving…' : 'Approve this draft'}</button>
              <button type="button" disabled={busy} onClick={() => setStep('edit')}>Edit again</button>
              <button type="button" disabled={busy} onClick={onClose}>Cancel</button>
            </div>
            {error && <p role="alert">{error}</p>}
          </div>
        )}
      </div>
    </div>
  )
}
