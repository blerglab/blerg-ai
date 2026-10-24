// useCapabilities: a session's latest capabilities report. Live and replayed
// events carry it, but a browser replays only the first window of a long
// transcript, so the latest is also asked for directly (each time `active`
// turns on: the session panel's Capabilities tab coming on screen); whichever is
// newer wins.
import { useEffect, useMemo, useRef, useState } from 'react'
import { apiFetch } from '../apiFetch'
import type { AgentEvent } from '../types'
import { latestCapabilities, newerReport, normalizeCapabilities, type CapabilitiesReport } from '../lib/capabilities'

export function useCapabilities(sessionId: string, events: AgentEvent[], active = true): CapabilitiesReport | null {
  // Keyed by session, so a switch never shows the previous session's report.
  const [fetched, setFetched] = useState<{ sessionId: string; report: CapabilitiesReport } | null>(null)
  const generation = useRef(0)
  const [scope, setScope] = useState(0)
  useEffect(() => {
    const invalidate = () => {
      generation.current++
      setFetched(null)
      setScope(value => value + 1)
    }
    window.addEventListener('blerg-desktop-scope-changed', invalidate)
    return () => window.removeEventListener('blerg-desktop-scope-changed', invalidate)
  }, [])
  useEffect(() => {
    const epoch = generation.current
    if (!active) return
    let live = true
    void apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/capabilities`)
      .then(r => (r.ok ? r.json() : null))
      .then((body: unknown) => {
        if (!live || epoch !== generation.current || !body || typeof body !== 'object') return
        const b = body as { capabilities?: unknown; seq?: unknown; ts?: unknown }
        const payload = normalizeCapabilities(b.capabilities)
        if (payload) {
          setFetched({
            sessionId,
            report: { payload, seq: typeof b.seq === 'number' ? b.seq : 0, ts: typeof b.ts === 'string' ? b.ts : '' },
          })
        } else {
          setFetched(null)
        }
      })
      .catch(() => {})
    return () => { live = false }
  }, [sessionId, active, scope])
  const fromEvents = useMemo(() => latestCapabilities(events), [events])
  return newerReport(fetched?.sessionId === sessionId ? fetched.report : null, fromEvents)
}
