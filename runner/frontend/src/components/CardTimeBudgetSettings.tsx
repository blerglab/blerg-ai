import { useEffect, useId, useState } from 'react'
import { apiFetch } from '../apiFetch'

// BLG-444, owner's decision 4 (corrected): the budget interval is the signed-in person's own
// setting, not install-wide — GET/PUT /api/settings/card-time-budget (card_time_settings.go)
// scope to the caller's own account, so this never reads or writes anyone else's.
export default function CardTimeBudgetSettings() {
  const [hours, setHours] = useState<number | null>(null)
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState(false)
  const id = useId()
  useEffect(() => {
    let active = true
    apiFetch('/api/settings/card-time-budget').then(async r => {
      if (!r.ok) return
      const data = await r.json() as { interval_hours: number }
      if (active && Number.isInteger(data.interval_hours)) setHours(data.interval_hours)
    }).catch(() => {})
    return () => { active = false }
  }, [])
  if (hours === null) return null
  const save = async () => {
    if (!Number.isInteger(hours) || hours < 1 || hours > 168) {
      setMessage('Enter a whole number of hours from 1 to 168.')
      return
    }
    setBusy(true); setMessage('')
    try {
      const r = await apiFetch('/api/settings/card-time-budget', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ interval_hours: hours }) })
      if (!r.ok) throw new Error()
      setMessage('Card time budget saved.')
    } catch { setMessage('Could not save the card time budget. Try again.') }
    finally { setBusy(false) }
  }
  return <section className="settings-section st-section" aria-labelledby={`${id}-heading`}>
    <h3 className="settings-heading st-heading" id={`${id}-heading`}>Card time budget</h3>
    <p className="settings-description">How often Main hears a card's worker time and review rounds, once a card crosses its first interval.</p>
    <label className="settings-field cc-field">
      <span>Interval (hours)</span>
      <input className="settings-field" type="number" min={1} max={168} step={1} value={hours} onChange={e => setHours(Number(e.target.value))} />
    </label>
    <p className="st-hint">Default: 4 hours, then every further 4 hours.</p>
    <button type="button" className="settings-action btn primary" disabled={busy} onClick={() => void save()}>{busy ? 'Saving…' : 'Save card time budget'}</button>
    {message && <p role="status" className="st-hint">{message}</p>}
  </section>
}
