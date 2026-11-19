import { useState } from 'react'
import type { SessionInfo } from '../types'
import { ChildDiscardAction } from './ChildDiscard'
import { blergTransport } from '../lib/chatTransport'

/** Every operation is bound to this worker; no workspace/header selection is consulted. */
export default function FailedWorkerActions({ session }: { session: SessionInfo }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  if (!session.parent_session_id || session.status !== 'error' || session.archived_at) return null
  // A terminal launch refusal needs its settings fixed and a fresh start by Main.
  // Only a revivable session can accept another message through the existing input path.
  const retryable = !session.ended_at && !session.end_reason
  async function retry() {
    setBusy(true); setError('')
    try {
      const result = await blergTransport().send(session.id, 'Retry starting this worker.')
      if (!result.queued) setError('Could not reach this worker. Try again when connected.')
    } catch { setError('Could not retry this worker. Try again when connected.') }
    finally { setBusy(false) }
  }
  return <>
    {retryable && <button type="button" className="sp-btn" disabled={busy} aria-busy={busy || undefined} onClick={() => void retry()}>Retry</button>}
    <ChildDiscardAction parentId={session.parent_session_id} childId={session.id} title={session.title || 'Untitled worker'} label="Discard worker" />
    {error && <span className="sp-stop-error" role="alert">{error}</span>}
  </>
}
