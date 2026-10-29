import { useEffect, useId, useState } from 'react'
import { apiFetch } from '../apiFetch'

export default function UsageNoticeSettings() {
  const [thresholds, setThresholds] = useState<number[] | null>(null)
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState(false)
  const id = useId()
  useEffect(() => {
    let active = true
    apiFetch('/api/settings/usage-notices').then(async r => {
      if (!r.ok) return // Administrator settings are hidden from other accounts.
      const data = await r.json() as { thresholds: number[] }
      if (active && Array.isArray(data.thresholds) && data.thresholds.length === 3) setThresholds(data.thresholds)
    }).catch(() => {})
    return () => { active = false }
  }, [])
  if (!thresholds) return null
  const save = async () => {
    if (!thresholds.every((n, i) => Number.isInteger(n) && n >= 1 && n <= 100 && (i === 0 || n > thresholds[i - 1]))) {
      setMessage('Enter three increasing whole percentages between 1 and 100.')
      return
    }
    setBusy(true); setMessage('')
    try {
      const r = await apiFetch('/api/settings/usage-notices', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ thresholds }) })
      if (!r.ok) throw new Error()
      setMessage('Usage notice thresholds saved for this install.')
    } catch { setMessage('Could not save usage notice thresholds. Try again.') }
    finally { setBusy(false) }
  }
  return <section className="settings-section st-section" aria-labelledby={`${id}-heading`}>
    <h3 className="settings-heading st-heading" id={`${id}-heading`}>Usage-limit notices</h3>
    <p className="settings-description">Install-wide thresholds for notices to working top-level sessions. These notices guide agents as an engine approaches its limit.</p>
    {['Prefer other engines', 'Start nothing new', 'Save state'].map((label, i) => <label className="settings-field cc-field" key={label}>
      <span>{label} (%)</span>
      <input className="settings-field" type="number" min={1} max={100} step={1} value={thresholds[i]} onChange={e => setThresholds(t => t!.map((n, j) => j === i ? Number(e.target.value) : n))} />
    </label>)}
    <p className="st-hint">Defaults: 80%, 90%, 95%. Sidebar meter colors always use these default levels.</p>
    <button type="button" className="settings-action btn primary" disabled={busy} onClick={() => void save()}>{busy ? 'Saving…' : 'Save usage thresholds'}</button>
    {message && <p role="status" className="st-hint">{message}</p>}
  </section>
}
