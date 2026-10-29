import { useEffect, useRef, useState } from 'react'
import { apiFetch } from '../apiFetch'
import { sessionView } from '../lib/sessionView'
import './UsageLimitResume.css'

/** Whether Blerg tells this session's agent when its pins or checklist drift from what it does
 *  (card #158). The owner's alone: anyone else gets 404 and sees nothing. On by default. */
export default function PlanNudges({ sessionId, active = true }: { sessionId: string; active?: boolean }) {
  // The last read of this session (the explorer and panel cache) shows at once; it is read again
  // when the tab is on screen. Without one, it is read at once.
  const [cached] = useState(() => sessionView<{ enabled: boolean | null }>(sessionId, 'plan-nudges').cached)
  const [enabled, setEnabled] = useState<boolean | null>(() => cached?.enabled ?? null)
  const read = useRef(false)
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const mounted = useRef(false)
  const endpoint = `/api/sessions/${encodeURIComponent(sessionId)}/plan-nudges`

  useEffect(() => {
    mounted.current = true
    return () => { mounted.current = false }
  }, [])
  // Read once per visit: at once, or (with a cached value on screen) when the tab is.
  useEffect(() => {
    if (read.current || (cached && !active)) return
    read.current = true
    const cache = sessionView<{ enabled: boolean | null }>(sessionId, 'plan-nudges')
    const ac = new AbortController()
    let finished = false
    void apiFetch(endpoint, { signal: ac.signal }).then(async response => {
      if (ac.signal.aborted) return
      if (response.status === 404 || response.status === 409) { setEnabled(null); cache.keep({ enabled: null }); return }
      if (!response.ok) throw new Error('load')
      const body = await response.json() as { enabled?: unknown }
      if (!ac.signal.aborted) { setEnabled(body.enabled === true); cache.keep({ enabled: body.enabled === true }) }
    }).catch(() => { if (!ac.signal.aborted) setError('Could not load the plan nudge setting.') })
      .finally(() => { finished = true })
    // Leaving (or StrictMode's replay of this effect) ends a read still out, and makes it eligible
    // to start again: the next setup reads afresh instead of keeping an aborted read.
    return () => { if (!finished) { ac.abort(); read.current = false } }
  }, [endpoint, sessionId, cached, active])

  async function toggle(next: boolean) {
    setSaving(true)
    setError('')
    // Bound before the save starts: its answer is kept under the account (and cache epoch) it was
    // made in, never under whoever is signed in when it lands.
    const cache = sessionView<{ enabled: boolean | null }>(sessionId, 'plan-nudges')
    try {
      const response = await apiFetch(endpoint, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ enabled: next }),
      })
      if (!response.ok) throw new Error('save')
      const body = await response.json() as { enabled?: unknown }
      cache.keep({ enabled: body.enabled === true })
      if (mounted.current) setEnabled(body.enabled === true)
    } catch {
      if (mounted.current) setError('Could not update the plan nudge setting. Try again.')
    } finally {
      if (mounted.current) setSaving(false)
    }
  }

  if (enabled === null) return error ? <span role="alert">{error}</span> : null
  return <span className="usage-limit-resume">
    <label className="rail-check" title="At the end of a turn, Blerg tells the agent when its pinned tickets or checklist no longer match what it is doing, with the commands that fix it.">
      <input type="checkbox" checked={enabled} disabled={saving}
        onChange={event => { void toggle(event.target.checked) }} />
      <span>Nudge when the plan drifts</span>
    </label>
    {error && <span role="alert">{error}</span>}
  </span>
}
