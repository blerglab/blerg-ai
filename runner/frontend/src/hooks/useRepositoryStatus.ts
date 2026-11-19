import { useEffect, useState } from 'react'
import { onOpen } from '../ws'
import { RefreshScheduler, watchTurnEnds } from '../lib/explorerAutoRefresh'
import { applyRepositoryStatus, emptyRepositoryStatus, readRepositoryStatus } from '../lib/repositoryStatus'
import type { RepositoryStatusSnapshot } from '../lib/repositoryStatus'

/** Status for a graph header. Busy reads retry quietly; hiding/unmounting cancels work. */
export function useRepositoryStatus(sessionId: string, path: string, active: boolean) {
  const scope = JSON.stringify([sessionId, path])
  const [snapshot, setSnapshot] = useState<{ scope: string; value: RepositoryStatusSnapshot }>({ scope: '', value: emptyRepositoryStatus })
  const [version, setVersion] = useState(0)
  useEffect(() => {
    if (!active) return
    const aborter = new AbortController()
    let retry: ReturnType<typeof setTimeout> | undefined
    const scheduler = new RefreshScheduler(async () => {
      clearTimeout(retry)
      try {
        const next = await readRepositoryStatus(sessionId, path, aborter.signal)
        if (aborter.signal.aborted) return 'cancelled'
        setSnapshot(previous => ({ scope, value: applyRepositoryStatus(previous.scope === scope ? previous.value : emptyRepositoryStatus, next) }))
        if (next.kind === 'busy') retry = setTimeout(() => scheduler.request(), 2000)
      } catch {
        if (aborter.signal.aborted) return 'cancelled'
        setSnapshot(previous => ({ scope, value: applyRepositoryStatus(previous.scope === scope ? previous.value : emptyRepositoryStatus, { kind: 'failure', reason: 'The repository state could not be read.' }) }))
      }
      return 'done'
    }, () => document.visibilityState === 'hidden' ? 'hidden' : true)
    const request = () => scheduler.request()
    const visibility = () => { if (document.visibilityState !== 'hidden') scheduler.request() }
    const stopTurns = watchTurnEnds(sessionId, request)
    const stopReconnect = onOpen(request)
    window.addEventListener('focus', request)
    document.addEventListener('visibilitychange', visibility)
    scheduler.request()
    return () => {
      aborter.abort()
      scheduler.stop()
      clearTimeout(retry)
      stopTurns()
      stopReconnect()
      window.removeEventListener('focus', request)
      document.removeEventListener('visibilitychange', visibility)
    }
  }, [sessionId, path, active, scope, version])
  return { ...(snapshot.scope === scope ? snapshot.value : emptyRepositoryStatus), refresh: () => setVersion(value => value + 1) }
}
