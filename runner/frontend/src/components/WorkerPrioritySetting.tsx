import { useEffect, useState } from 'react'
import { apiFetch } from '../apiFetch'

interface Policy { enabled: boolean; backend: string }

export default function WorkerPrioritySetting({ daemonId }: { daemonId: string }) {
 const [policy, setPolicy] = useState<Policy | null>(null)
 const [busy, setBusy] = useState(false)
 const [error, setError] = useState('')
 useEffect(() => {
  let active = true
  void apiFetch(`/api/daemons/${encodeURIComponent(daemonId)}/worker-priority`).then(async response => {
   if (!response.ok) throw new Error('Update or reconnect this desktop to change worker priority.')
   const result = await response.json() as Policy
   if (active) setPolicy(result)
  }).catch((err: unknown) => { if (active) setError(err instanceof Error ? err.message : 'Could not read worker priority.') })
  return () => { active = false }
 }, [daemonId])
 async function change(enabled: boolean) {
  setBusy(true); setError('')
  try {
   const response = await apiFetch(`/api/daemons/${encodeURIComponent(daemonId)}/worker-priority`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ enabled }) })
   if (!response.ok) throw new Error('Could not save worker priority. Try again.')
   setPolicy(await response.json() as Policy)
  } catch (err) { setError(err instanceof Error ? err.message : 'Could not save worker priority.') }
  finally { setBusy(false) }
 }
 return <div className="settings-section st-section">
  <label className="settings-field"><input className="settings-toggle-control" type="checkbox" checked={policy?.enabled ?? false} disabled={!policy || busy} onChange={e => void change(e.target.checked)} /> Give your interactive work priority</label>
  <p className="settings-description st-hint">New desktop agent sessions, tools, gates, and test browsers yield CPU and disk access to your apps. All agent work keeps equal priority. Cluster pods use their existing limits.</p>
  {policy && <p className="settings-description st-hint">{policy.backend === 'unavailable' ? 'Scheduling controls are unavailable on this desktop.' : `Available controls: ${policy.backend}.`} Changes apply to new sessions.</p>}
  {error && <p role="alert" className="settings-description st-hint">{error}</p>}
 </div>
}
