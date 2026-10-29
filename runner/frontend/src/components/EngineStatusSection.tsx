// Settings → Engines: for each of your connected desktops, every known engine with whether it is
// ready and, when not, why and the exact fix — the same rows the launch sheet shows, plus
// "Check again" and "Re-detect PATH" for the whole desktop.
import { useCallback, useEffect, useState } from 'react'
import { apiFetch } from '../apiFetch'
import { ENGINE_LABELS } from '../lib/runtimes'
import { checkEngines, parseEngineRows, type EngineStatusRow } from '../lib/engineStatus'
import EngineStatusList from './EngineStatusList'

interface Desktop { id: string; name: string; rows: EngineStatusRow[] | null }

async function fetchDesktops(): Promise<Desktop[]> {
  const res = await apiFetch('/api/daemons')
  if (!res.ok) throw new Error('Could not load your desktops.')
  const data: unknown = await res.json()
  if (!Array.isArray(data)) throw new Error('Could not load your desktops.')
  return data.flatMap((d: unknown) => {
    if (!d || typeof d !== 'object') return []
    const r = d as Record<string, unknown>
    if (typeof r.id !== 'string' || typeof r.name !== 'string') return []
    return [{ id: r.id, name: r.name, rows: Array.isArray(r.engine_statuses) ? parseEngineRows(r.engine_statuses, ENGINE_LABELS) : null }]
  })
}

export default function EngineStatusSection() {
  const [desktops, setDesktops] = useState<Desktop[] | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const [notes, setNotes] = useState<Record<string, string>>({})
  const refresh = useCallback(() => {
    void fetchDesktops().then(list => { setDesktops(list); setError('') })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'Could not load your desktops.'))
  }, [])
  useEffect(() => {
    refresh()
    window.addEventListener('blerg-desktop-inventory-changed', refresh)
    return () => window.removeEventListener('blerg-desktop-inventory-changed', refresh)
  }, [refresh])

  async function check(desktop: Desktop, redetectPath: boolean) {
    if (busy) return
    setBusy(`${desktop.id}:${redetectPath}`)
    setNotes(n => ({ ...n, [desktop.id]: redetectPath ? 'Reading your login shell’s PATH…' : 'Checking…' }))
    try {
      const outcome = await checkEngines(desktop.id, redetectPath, ENGINE_LABELS)
      setDesktops(list => list?.map(d => d.id === desktop.id ? { ...d, rows: outcome.rows } : d) ?? list)
      const ready = outcome.rows.filter(r => r.status === 'available').map(r => r.name)
      let note = ready.length ? `Ready: ${ready.join(', ')}.` : 'No engine is ready yet.'
      if (redetectPath) note = outcome.pathError ?? ((outcome.pathAdded.length ? `Added ${outcome.pathAdded.join(', ')} to the daemon’s PATH. ` : 'No new folders on your login shell’s PATH. ') + note)
      setNotes(n => ({ ...n, [desktop.id]: note }))
    } catch (err) {
      setNotes(n => ({ ...n, [desktop.id]: err instanceof Error ? err.message : 'Check failed. Try again.' }))
    } finally {
      setBusy('')
    }
  }

  return <section className="settings-section st-section" aria-label="Engines" data-testid="settings-engines">
    <h3 className="settings-heading st-heading">Engines</h3>
    <p className="settings-description st-hint">
      Which coding engines each of your desktops can run, and how to fix the ones it can’t. Installed or logged in to one
      after installing Blerg? Choose Check again. If the desktop can’t find a CLI your terminal finds, choose Re-detect PATH.
    </p>
    {error && <p role="alert">{error}</p>}
    {!desktops && !error && <p role="status">Loading your desktops…</p>}
    {desktops?.length === 0 && <p className="settings-description st-hint">No desktop is connected. Start one with daemon/install.sh install.</p>}
    {desktops?.map(desktop => <div key={desktop.id} className="settings-section st-section engine-status-desktop" data-testid={`engines-${desktop.id}`}>
      <p><strong>{desktop.name}</strong></p>
      {desktop.rows === null
        ? <p className="settings-description st-hint">This desktop’s daemon is older and reports only the engines that work. Update it (daemon/install.sh install) to see why the others don’t.</p>
        : <EngineStatusList rows={desktop.rows} daemonId={desktop.id} actions={false} onChecked={refresh} testId={`engine-status-list-${desktop.id}`} />}
      <div className="engine-status-actions">
        <button type="button" className="settings-action btn small" disabled={!!busy} onClick={() => void check(desktop, false)}>
          {busy === `${desktop.id}:false` ? 'Checking…' : 'Check again'}
        </button>
        <button type="button" className="settings-action btn small" disabled={!!busy} onClick={() => void check(desktop, true)}>
          {busy === `${desktop.id}:true` ? 'Re-detecting…' : 'Re-detect PATH'}
        </button>
      </div>
      {notes[desktop.id] && <p role="status" className="engine-status-note">{notes[desktop.id]}</p>}
    </div>)}
  </section>
}
