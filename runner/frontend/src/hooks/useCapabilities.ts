// useCapabilities: a session's latest capabilities report. Live and replayed
// events carry it, but a browser replays only the first window of a long
// transcript, so the latest is also asked for directly; whichever is newer
// wins.
import { useEffect, useMemo, useState } from 'react'
import { apiFetch } from '../apiFetch'
import type { AgentEvent } from '../types'
import { latestCapabilities, newerReport, normalizeCapabilities, type CapabilitiesReport } from '../lib/capabilities'

export function useCapabilities(sessionId: string, events: AgentEvent[]): CapabilitiesReport | null {
  // Keyed by session, so a switch never shows the previous session's report.
  const [fetched, setFetched] = useState<{ sessionId: string; report: CapabilitiesReport } | null>(null)
  useEffect(() => {
    let live = true
    void apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/capabilities`)
      .then(r => (r.ok ? r.json() : null))
      .then((body: unknown) => {
        if (!live || !body || typeof body !== 'object') return
        const b = body as { capabilities?: unknown; seq?: unknown; ts?: unknown }
        const payload = normalizeCapabilities(b.capabilities)
        if (payload) {
          setFetched({
            sessionId,
            report: { payload, seq: typeof b.seq === 'number' ? b.seq : 0, ts: typeof b.ts === 'string' ? b.ts : '' },
          })
        }
      })
      .catch(() => {})
    return () => { live = false }
  }, [sessionId])
  const fromEvents = useMemo(() => latestCapabilities(events), [events])
  return newerReport(fetched?.sessionId === sessionId ? fetched.report : null, fromEvents)
}
