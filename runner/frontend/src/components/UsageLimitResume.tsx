import { useEffect, useRef, useState } from 'react'
import { apiFetch } from '../apiFetch'
import { sessionView } from '../lib/sessionView'
import './UsageLimitResume.css'

interface ResumeState {
  enabled: boolean
  state: string
  reason: string
  reset_at?: string
  attempts: number
}

/** Owner-only unattended continuation controls; the server remains the authority. */
export default function UsageLimitResume({ sessionId }: { sessionId: string }) {
  // The last read of this session (the explorer and panel cache) shows at once; it polls as before.
  const [value, setValue] = useState<ResumeState | null>(() => sessionView<ResumeState | null>(sessionId, 'limit-resume').cached ?? null)
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const revision = useRef(0)
  const busy = useRef(false)
  const mounted = useRef(false)
  const endpoint = `/api/sessions/${encodeURIComponent(sessionId)}/limit-resume`

  useEffect(() => {
    mounted.current = true
    let active = true
    let reading: AbortController | null = null
    const load = async () => {
      if (busy.current) return
      const version = ++revision.current
      const cache = sessionView<ResumeState | null>(sessionId, 'limit-resume')
      reading?.abort()
      const ac = new AbortController()
      reading = ac
      try {
        const response = await apiFetch(endpoint, { signal: ac.signal })
        if (!active || version !== revision.current) return
        if (response.status === 404) { setValue(null); setError(''); cache.keep(null); return }
        if (!response.ok) throw new Error('Could not load automatic resume settings.')
        const next = await response.json() as ResumeState
        if (active && version === revision.current) { setValue(next); setError(''); cache.keep(next) }
      } catch {
        if (active && version === revision.current) setError('Could not load automatic resume settings.')
      }
    }
    void load()
    const timer = window.setInterval(() => { void load() }, 15_000)
    return () => { active = false; mounted.current = false; reading?.abort(); window.clearInterval(timer) }
  }, [endpoint, sessionId])

  async function toggle(enabled: boolean) {
    busy.current = true
    ++revision.current
    setSaving(true)
    setError('')
    try {
      const response = await apiFetch(endpoint, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ enabled }),
      })
      if (!mounted.current) return
      if (response.status === 404) { setValue(null); return }
      if (!response.ok) throw new Error('save')
      const fresh = await apiFetch(endpoint)
      if (!mounted.current) return
      if (fresh.status === 404) { setValue(null); return }
      if (!fresh.ok) throw new Error('refresh')
      const next = await fresh.json() as ResumeState
      if (mounted.current) setValue(next)
    } catch {
      if (mounted.current) setError('Could not update automatic resume settings. Try again.')
    } finally {
      busy.current = false
      if (mounted.current) setSaving(false)
    }
  }

  if (!value) return error ? <span role="alert">{error}</span> : null
  let status = ''
  if (value.enabled) {
    if (value.state === 'pending' && value.reset_at) {
      status = `Resumes after ${new Date(value.reset_at).toLocaleString()}`
    } else if (value.state === 'unknown' || value.state === 'unknown_reset') {
      status = 'Reset time unknown; checking the usage meter.'
    } else if (value.state === 'blocked') {
      status = 'Automatic resume blocked; continue manually.'
    } else if (value.state === 'exhausted') {
      status = 'Automatic resume reached its retry limit; continue manually.'
    } else if (value.state === 'expired') {
      status = 'The continuation window expired; continue manually.'
    }
    if (value.reason) status = status ? `${status} ${value.reason}` : value.reason
  }
  return <span className="usage-limit-resume">
    <label className="rail-check" title="Allow this session to continue automatically after a provider usage limit resets.">
      <input type="checkbox" checked={value.enabled} disabled={saving}
        onChange={event => { void toggle(event.target.checked) }} />
      <span>Resume when limit resets</span>
    </label>
    {status && <span role="status">{status}</span>}
    {error && <span role="alert">{error}</span>}
  </span>
}
