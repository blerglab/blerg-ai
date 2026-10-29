// useArtifacts: a session's published files, newest first. Read through the transport when the view
// opens, and again whenever a new `artifact` or `session_upload` event reaches the transcript (live or replayed), so
// the Files button's count and the panel follow what the agent publishes.
import { useCallback, useEffect, useMemo, useState } from 'react'
import type { ArtifactInfo, TransportFiles } from '../transport/types'
import type { AgentEvent } from '../types'
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

/** With no file operations on the transport the list is empty and 'ready'. */
export function useArtifacts(sessionId: string, files: TransportFiles | null, events: AgentEvent[]): Artifacts {
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
  if (!files) return { items: EMPTY, status: 'ready', refresh }
  const kept = loaded?.sessionId === sessionId ? null : peekList<ArtifactInfo[]>(files, sessionId)
  const cur: Loaded = loaded?.sessionId === sessionId ? loaded : kept ? { sessionId, items: kept, status: 'ready' } : { sessionId, items: EMPTY, status: 'loading' }
  return { items: cur.items, status: cur.status, refresh }
}

const EMPTY: ArtifactInfo[] = []
