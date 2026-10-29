// useCapabilities: a session's latest capabilities report. Live and replayed
// events carry it, but a browser replays only the first window of a long
// transcript, so the latest is also asked for directly (each time `active`
// turns on: the session panel's Capabilities tab coming on screen); whichever is
// newer wins.
import { useEffect, useMemo, useRef, useState } from 'react'
import { apiFetch } from '../apiFetch'
import { sessionView } from '../lib/sessionView'
import type { AgentEvent } from '../types'
import { latestCapabilities, newerReport, normalizeCapabilities, type CapabilitiesReport } from '../lib/capabilities'

export function useCapabilities(sessionId: string, events: AgentEvent[], active = true): CapabilitiesReport | null {
  return useCapabilitiesState(sessionId, events, active).report
}

export function useCapabilitiesState(sessionId: string, events: AgentEvent[], active = true): { report: CapabilitiesReport | null; loading: boolean } {
  // Inventory and scope changes refresh the read, but only a session's first read can load.
  const [settledFor, setSettledFor] = useState(() => sessionView<CapabilitiesReport>(sessionId, 'capabilities').cached ? sessionId : '')
  // Keyed by session, so a switch never shows the previous session's report.
  // The last report read for this session (the explorer and panel cache) shows at once.
  const [fetched, setFetched] = useState<{ sessionId: string; report: CapabilitiesReport } | null>(() => {
    const report = sessionView<CapabilitiesReport>(sessionId, 'capabilities').cached
    return report ? { sessionId, report } : null
  })
  const generation = useRef(0)
  const [scope, setScope] = useState(0)
  useEffect(() => {
    const invalidate = () => {
      generation.current++
      setFetched(null)
      setScope(value => value + 1)
    }
    const refresh = () => { generation.current++; setScope(value => value + 1) }
    window.addEventListener('blerg-desktop-inventory-changed', refresh)
    window.addEventListener('blerg-desktop-scope-changed', invalidate)
    return () => { window.removeEventListener('blerg-desktop-scope-changed', invalidate); window.removeEventListener('blerg-desktop-inventory-changed', refresh) }
  }, [])
  useEffect(() => {
    const epoch = generation.current
    if (!active) return
    let live = true
    const cache = sessionView<CapabilitiesReport>(sessionId, 'capabilities')
    const ac = new AbortController()
    void apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/capabilities`, { signal: ac.signal })
      .then(r => (r.ok ? r.json() : null))
      .then((body: unknown) => {
        if (!live || epoch !== generation.current || !body || typeof body !== 'object') return
        const b = body as { capabilities?: unknown; seq?: unknown; ts?: unknown }
        const payload = normalizeCapabilities(b.capabilities)
        if (payload) {
          const report = { payload, seq: typeof b.seq === 'number' ? b.seq : 0, ts: typeof b.ts === 'string' ? b.ts : '' }
          setFetched({ sessionId, report })
          cache.keep(report)
        } else {
          setFetched(null)
        }
      })
      .catch(() => {})
      .finally(() => { if (live && epoch === generation.current) setSettledFor(sessionId) })
    return () => { live = false; ac.abort() }
  }, [sessionId, active, scope])
  const fromEvents = useMemo(() => latestCapabilities(events), [events])
  const report = newerReport(fetched?.sessionId === sessionId ? fetched.report : null, fromEvents)
  return { report, loading: !report && settledFor !== sessionId }
}
