// useArtifacts: a session's published files, newest first. Read through the transport when the view
// opens, and again whenever a new `artifact` or `session_upload` event reaches the transcript (live or replayed), so
// the Files button's count and the panel follow what the agent publishes.
//
// A live file's (BLG-470) own republish never reaches events (it is pushed outside the transcript,
// never persisted: useSession's onArtifactUpdated -> useTranscriptStore's liveArtifacts, not
// ingest) — republishing it often must not re-read the whole list every time. liveArtifacts'
// patches are merged over whatever the list last held, by id, and only applied when strictly
// newer than what the list already has (a patch that arrives after a fresher list read must not
// resurrect stale metadata). The merge is memoized on (list, patches): a push changes the returned
// items reference exactly once, not on every unrelated render (a streaming delta included) —
// ChatView and its siblings build big useMemo values keyed on these items, so a churning
// reference there would invalidate them broadly.
import { useCallback, useEffect, useMemo, useState } from 'react'
import type { ArtifactInfo, TransportFiles } from '../transport/types'
import type { AgentEvent, ArtifactPayload } from '../types'
import { listKeeper, peekList } from '../model/viewCache'

export type ArtifactsStatus = 'loading' | 'ready' | 'error'

interface Loaded {
  sessionId: string
  items: ArtifactInfo[]
  status: ArtifactsStatus
}

export interface Artifacts {
  items: ArtifactInfo[]
  status: ArtifactsStatus
  /** Reads the list again (an upload or a delete leaves no transcript event). */
  refresh: () => void
}

/** Whether patch is strictly newer than what the list already holds for the same id: by
 *  updated_at, with sha256 as a tie-breaker when both timestamps are equal (two republishes close
 *  enough to share a timestamp, or two patches racing in). The tie-break is an arbitrary total
 *  order (lexicographic), not a real precedence — it only has to agree with itself, so applying
 *  two ties in either order converges on the same winner. A patch with no parseable updated_at
 *  never counts as newer; one against a current row with no parseable updated_at always does. */
function isNewer(patch: ArtifactPayload, current: Pick<ArtifactPayload, 'updated_at' | 'sha256'>): boolean {
  const patchTime = Date.parse(patch.updated_at ?? '')
  const curTime = Date.parse(current.updated_at ?? '')
  if (!Number.isFinite(patchTime)) return false
  if (!Number.isFinite(curTime)) return true
  if (patchTime !== curTime) return patchTime > curTime
  return (patch.sha256 ?? '') > (current.sha256 ?? '')
}

interface Merged {
  items: ArtifactInfo[]
  /** Ids of patches that are no longer useful: the list has caught up with them (its own row is
   *  at least as new), or the id they name is not in the list at all (the file is gone). Callers
   *  prune these so the patch store does not grow across a long session's republishes. */
  caughtUp: string[]
}

/** Overlays each live patch that is strictly newer onto the list item of the same id (size,
 *  content type, view, live, updated_at, sha256): the fields a republish can change. items keeps
 *  its reference when no patch changes anything, so a caller comparing by reference sees no
 *  spurious update. */
function withLiveArtifacts(items: ArtifactInfo[], liveArtifacts: Record<string, ArtifactPayload>): Merged {
  const ids = Object.keys(liveArtifacts)
  if (ids.length === 0) return { items, caughtUp: EMPTY_IDS }
  const present = new Set<string>()
  let changed = false
  const caughtUp: string[] = []
  const next = items.map(a => {
    const patch = liveArtifacts[a.id]
    if (!patch) return a
    present.add(a.id)
    if (!isNewer(patch, a)) {
      caughtUp.push(a.id)
      return a
    }
    changed = true
    return { ...a, size: patch.size, content_type: patch.content_type, view: patch.view, live: patch.live ?? a.live, updated_at: patch.updated_at, sha256: patch.sha256 }
  })
  for (const id of ids) {
    if (!present.has(id)) caughtUp.push(id) // no list item names this id: the file is gone
  }
  return { items: changed ? next : items, caughtUp }
}

/** With no file operations on the transport the list is empty and 'ready'. */
export function useArtifacts(
  sessionId: string,
  files: TransportFiles | null,
  events: AgentEvent[],
  liveArtifacts: Record<string, ArtifactPayload> = EMPTY_LIVE,
  pruneLiveArtifacts?: (sessionId: string, ids: string[]) => void,
): Artifacts {
  // The list last read for this session (model/viewCache) shows at once and is read again behind it.
  const [loaded, setLoaded] = useState<Loaded | null>(() => {
    const items = files ? peekList<ArtifactInfo[]>(files, sessionId) : undefined
    return items ? { sessionId, items, status: 'ready' } : null
  })
  const [reloads, setReloads] = useState(0)
  const published = useMemo(() => events.reduce((n, ev) => n + (ev.kind === 'artifact' || ev.kind === 'session_upload' ? 1 : 0), 0), [events])

  useEffect(() => {
    if (!files) return
    let live = true
    const keep = listKeeper<ArtifactInfo[]>(files, sessionId)
    files.list(sessionId).then(
      items => { keep(items); if (live) setLoaded({ sessionId, items, status: 'ready' }) },
      () => { if (live) setLoaded(prev => ({ sessionId, items: prev?.sessionId === sessionId ? prev.items : [], status: 'error' })) },
    )
    return () => { live = false }
  }, [sessionId, files, published, reloads])

  const refresh = useCallback(() => setReloads(n => n + 1), [])

  const kept = loaded?.sessionId === sessionId ? null : (files ? peekList<ArtifactInfo[]>(files, sessionId) : undefined)
  const cur: Loaded = loaded?.sessionId === sessionId ? loaded : kept ? { sessionId, items: kept, status: 'ready' } : { sessionId, items: EMPTY, status: files ? 'loading' : 'ready' }

  const merged = useMemo(() => withLiveArtifacts(cur.items, liveArtifacts), [cur.items, liveArtifacts])

  useEffect(() => {
    if (merged.caughtUp.length > 0) pruneLiveArtifacts?.(sessionId, merged.caughtUp)
  }, [merged.caughtUp, sessionId, pruneLiveArtifacts])

  if (!files) return { items: EMPTY, status: 'ready', refresh }
  return { items: merged.items, status: cur.status, refresh }
}

const EMPTY: ArtifactInfo[] = []
const EMPTY_IDS: string[] = []
const EMPTY_LIVE: Record<string, ArtifactPayload> = {}
