// useArtifacts: a session's published files, newest first. Read from the server when the view
// opens, and again whenever a new `artifact` event reaches the transcript (live or replayed), so
// the Files button's count and the panel follow what the agent publishes.
import { useCallback, useEffect, useMemo, useState } from 'react'
import { listArtifacts, type ArtifactInfo } from '../lib/artifacts'
import type { AgentEvent } from '../types'

export type ArtifactsStatus = 'loading' | 'ready' | 'error'

interface Loaded {
  sessionId: string
  items: ArtifactInfo[]
  status: ArtifactsStatus
}

/**
 * `refreshKey` is anything whose change should read the list again: files you upload leave no event in
 * the transcript, so the view passes how many are attached and whether the panel is open.
 */
export function useArtifacts(sessionId: string, events: AgentEvent[], refreshKey: string | number = 0) {
  const [loaded, setLoaded] = useState<Loaded | null>(null)
  const [reloads, setReloads] = useState(0)
  const published = useMemo(() => events.reduce((n, ev) => n + (ev.kind === 'artifact' ? 1 : 0), 0), [events])

  useEffect(() => {
    let live = true
    listArtifacts(sessionId).then(
      items => { if (live) setLoaded({ sessionId, items, status: 'ready' }) },
      () => { if (live) setLoaded(prev => ({ sessionId, items: prev?.sessionId === sessionId ? prev.items : [], status: 'error' })) },
    )
    return () => { live = false }
  }, [sessionId, published, reloads, refreshKey])

  const refresh = useCallback(() => setReloads(n => n + 1), [])
  const cur: Loaded = loaded?.sessionId === sessionId ? loaded : { sessionId, items: [], status: 'loading' }
  return { items: cur.items, status: cur.status, refresh }
}
