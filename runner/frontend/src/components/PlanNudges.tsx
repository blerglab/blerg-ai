import { useEffect, useRef, useState } from 'react'
import { apiFetch } from '../apiFetch'
import './UsageLimitResume.css'

/** Whether Blerg tells this session's agent when its pins or checklist drift from what it does
 *  (card #158). The owner's alone: anyone else gets 404 and sees nothing. On by default. */
export default function PlanNudges({ sessionId }: { sessionId: string }) {
  const [enabled, setEnabled] = useState<boolean | null>(null)
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const mounted = useRef(false)
  const endpoint = `/api/sessions/${encodeURIComponent(sessionId)}/plan-nudges`

  useEffect(() => {
    mounted.current = true
    let active = true
    void apiFetch(endpoint).then(async response => {
      if (!active) return
      if (response.status === 404 || response.status === 409) { setEnabled(null); return }
      if (!response.ok) throw new Error('load')
      const body = await response.json() as { enabled?: unknown }
      if (active) setEnabled(body.enabled === true)
    }).catch(() => { if (active) setError('Could not load the plan nudge setting.') })
    return () => { active = false; mounted.current = false }
  }, [endpoint])

  async function toggle(next: boolean) {
    setSaving(true)
    setError('')
    try {
      const response = await apiFetch(endpoint, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ enabled: next }),
      })
      if (!response.ok) throw new Error('save')
      const body = await response.json() as { enabled?: unknown }
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
