import { invalidateBlackboardViews } from './blackboardViewCache'
import { apiFetch } from '../apiFetch'
// Only the milestone fields the button needs to pick what to open.
export type BlackboardRecordSummary = { id: string; name: string; milestones: { id: string; name: string; frozen_at: string | null }[] }
// BLG-431: switching away from a session and back refetched this every mount with no cache at
// all, unlike cardTimeData.ts's pattern for the same kind of per-session read. A session's
// linked blackboards essentially never change between two switches a few seconds apart, so a
// short freshness window (matching cardTimeData.ts's FRESH_MS) avoids the repeat read and the
// button's flash-to-hidden-then-back while it's pending, without ever showing a stale list for
// long. Cleared on an identity change (a silent renewal can sign in someone else without a reload).
export const blackboardReads = new Map<string, { at: number; list: BlackboardRecordSummary[] }>()
const blackboardInflight = new Map<string, Promise<BlackboardRecordSummary[]>>()
const BLACKBOARDS_FRESH_MS = 60_000
// Bounded like useLiveStateServer.ts's KEEP: a long-lived page visiting many sessions doesn't grow it forever.
const KEEP = 20
window.addEventListener('blerg-identity-changed', () => { blackboardReads.clear(); blackboardInflight.clear() })
/** Test-only: clears the module-level cache between tests sharing it. */
export function resetBlackboardReadsForTest(): void { blackboardReads.clear(); blackboardInflight.clear(); invalidateBlackboardViews() }
export function readBlackboards(sessionId: string): Promise<BlackboardRecordSummary[]> {
 const held = blackboardReads.get(sessionId)
 if (held && Date.now() - held.at < BLACKBOARDS_FRESH_MS) return Promise.resolve(held.list)
 const running = blackboardInflight.get(sessionId)
 if (running) return running
 const p = apiFetch(`/api/blackboards?session=${encodeURIComponent(sessionId)}`)
  .then(async r => {
   if (!r.ok) throw Object.assign(new Error('Blackboard response failed'), { status: r.status })
   return ((await r.json()).blackboards ?? []) as BlackboardRecordSummary[]
  })
  .catch(error => { console.error('Could not read blackboards.', { cause: error instanceof TypeError ? 'network' : 'response', status: error?.status }); return [] as BlackboardRecordSummary[] })
  .then(list => {
   if (blackboardInflight.get(sessionId) !== p) return list
   blackboardReads.delete(sessionId)
   blackboardReads.set(sessionId, { at: Date.now(), list })
   if (blackboardReads.size > KEEP) blackboardReads.delete(blackboardReads.keys().next().value!)
   return list
  })
  .finally(() => { if (blackboardInflight.get(sessionId) === p) blackboardInflight.delete(sessionId) })
 blackboardInflight.set(sessionId, p)
 return p
}

/** Forget cached blackboards after one is created, so every session of the workspace shows it at once. */
export function invalidateBlackboardReads(sessionIds?: Iterable<string>): void {
 const ids = sessionIds ? [...sessionIds] : undefined
 invalidateBlackboardViews(ids)
 if (!ids) { blackboardReads.clear(); blackboardInflight.clear(); return }
 for (const id of ids) { blackboardReads.delete(id); blackboardInflight.delete(id) }
}

/** Reads this workspace's blackboards (usually zero or one). Shared by the header's always-shown
 *  BlackboardButton and the collapsed "Open" menu's BlackboardMenuItem (card BLG-450), so both agree
 *  on what, if anything, there is to open. */
