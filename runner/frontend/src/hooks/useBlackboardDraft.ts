import { useEffect, useState } from 'react'
import type { BlackboardDraftChanged } from '../types'
import { apiFetch } from '../apiFetch'
import { onMessage, onOpen } from '../ws'
import { blackboardOrigin, blackboardWorkspace, blackboardWorkspaceSessions } from '../lib/blackboardChanges'
import { invalidateBlackboardReads } from '../lib/blackboardReads'

export type DraftPlan = { name: string; vision: string; goals: string[]; milestone: { name: string; release_id?: string; target?: string }; board_id?: string; thrusts: { name: string; goal_index: number; reason: string }[] }

/** The existing browser bus invalidates reads for every tab in this workspace, including channels.
 * Reconnects refresh too: a draft may have changed while this browser was offline. */
export function useBlackboardRevision(sessionId: string): number {
 const [revision, setRevision] = useState(0)
 useEffect(() => {
  let timer: ReturnType<typeof setTimeout> | undefined
  const refresh = () => {
   invalidateBlackboardReads(blackboardWorkspaceSessions(sessionId))
   clearTimeout(timer)
   timer = setTimeout(() => setRevision(n => n + 1), 200)
  }
  const off = onMessage<BlackboardDraftChanged>('blackboard_draft_changed', event => {
   if (event.draft_readable === false || event.origin_id === blackboardOrigin(sessionId)) return
   if (blackboardWorkspace(sessionId) === event.session_id) refresh()
  })
  // onOpen also invokes a newly registered listener synchronously on an already-open
  // socket. That is a mount, not a reconnect: preserve the shared read cache.
  let registering = true
  const opened = onOpen(() => { if (!registering) refresh() })
  registering = false
  return () => { clearTimeout(timer); off(); opened() }
 }, [sessionId])
 return revision
}

export function useBlackboardDraft(sessionId: string, attempt = 0, onFailure?: () => void): DraftPlan | null | undefined {
 const revision = useBlackboardRevision(sessionId)
 const [draft, setDraft] = useState<DraftPlan | null>()
 useEffect(() => {
  let alive = true
  void apiFetch(`/api/blackboards/draft?session=${encodeURIComponent(sessionId)}`)
   .then(async r => {
    if (!r.ok) throw Object.assign(new Error('Blackboard draft response failed'), { status: r.status })
    const body = await r.json()
    if (alive) setDraft(body.draft?.untrusted_data ?? null)
   })
   .catch(error => {
    // Keep an already visible draft on a failed refresh; the server logs HTTP failures.
    if (alive) onFailure?.()
    console.error('Could not read the blackboard draft.', { cause: error instanceof TypeError ? 'network' : 'response', status: error?.status })
   })
  return () => { alive = false }
 }, [sessionId, attempt, revision, onFailure])
 return draft
}
