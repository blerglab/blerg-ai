import { useEffect, useState } from 'react'
import { apiFetch } from '../apiFetch'

type Desktop = { id: string; name: string; owner_id: string; ownership_revision: number }
type View = { desktops: Desktop[]; owners: { id: string; name: string }[] }

// The endpoint independently requires a human administrator login. Unknown
// desktops are administrative inventory, never ordinary launch targets.
export default function DesktopOwnershipSection() {
  const [view, setView] = useState<View | null>(null)
  const [choices, setChoices] = useState<Record<string, string>>({})
  const [error, setError] = useState('')
  const [saving, setSaving] = useState('')
  const [saved, setSaved] = useState(false)
  async function load() {
    const res = await apiFetch('/api/desktop-ownership')
    if (res.status === 403) return
    if (!res.ok) throw new Error('Could not load desktop ownership.')
    const data = await res.json() as View
    if (Array.isArray(data.desktops) && Array.isArray(data.owners)) setView(data)
  }
  useEffect(() => { let alive = true
    apiFetch('/api/desktop-ownership').then(async res => {
      if (res.status === 403) return
      if (!res.ok) throw new Error('Could not load desktop ownership.')
      const data = await res.json() as View
      if (alive && Array.isArray(data.desktops) && Array.isArray(data.owners)) setView(data)
    }).catch(() => { if (alive) setError('Could not load desktop ownership.') })
    return () => { alive = false }
  }, [])
  async function assign(desktop: Desktop) {
    const owner = choices[desktop.id]
    if (!owner || saving) return
    setSaving(desktop.id); setError(''); setSaved(false)
    try {
      const res = await apiFetch(`/api/desktop-ownership/${encodeURIComponent(desktop.id)}`, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ owner_id: owner, ownership_revision: desktop.ownership_revision }),
      })
      if (!res.ok) throw new Error('The desktop changed or assignment was refused. Refresh before trying again.')
      await load(); setSaved(true)
    } catch (err) { setError(err instanceof Error ? err.message : 'Could not assign the desktop.') }
    finally { setSaving('') }
  }
  if (!view && !error) return null
  return <section className="st-section" aria-label="Desktop ownership">
    <h3 className="st-heading">Desktop ownership</h3>
    <p className="st-hint">Assign an unowned desktop to a person. Reassignment disconnects its old connection and invalidates its credentials. Reinstall or enroll it for the new owner before launching work. Only unassigned desktops and your own desktops appear here.</p>
    {error && <p role="alert">{error}</p>}
    {saved && <p role="status">Owner recorded. Enroll the desktop for its new owner before reconnecting.</p>}
    {view?.desktops.map(desktop => <div key={desktop.id} className="st-section">
      <label className="st-field">{desktop.name} · {desktop.owner_id ? 'Assigned' : 'Unassigned'}
        <select className="select" aria-label={`Owner of ${desktop.name}`} value={choices[desktop.id] ?? ''}
          disabled={!!saving} onChange={e => setChoices({ ...choices, [desktop.id]: e.target.value })}>
          <option value="">Choose a person</option>
          {view.owners.map(owner => <option key={owner.id} value={owner.id}>{owner.name}</option>)}
        </select>
      </label>
      <button className="btn" disabled={!!saving || !choices[desktop.id]} onClick={() => void assign(desktop)}>{saving === desktop.id ? 'Assigning…' : 'Assign desktop'}</button>
    </div>)}
    {view?.desktops.length === 0 && <p className="st-hint">No unassigned desktops or desktops of your own.</p>}
  </section>
}
