import { useEffect, useId, useRef, useState } from 'react'
import { apiFetch } from '../apiFetch'
import { signedInAccount } from '../lib/liveBoardCard'

export type Verbosity = 'narrated' | 'standard' | 'terse'
export function VerbositySelect({ value, onChange, label = 'Verbosity', disabled = false, accountDefault = false }: {
 value: string; onChange: (value: Verbosity | '') => void; label?: string; disabled?: boolean; accountDefault?: boolean
}) {
 const id = useId()
 return <label htmlFor={id} style={{ display: 'block', fontSize: 12, maxWidth: '100%' }}>{label}
  <select id={id} className="settings-field" style={{ maxWidth: '100%', boxSizing: 'border-box' }} value={value} disabled={disabled} onChange={event => onChange(event.target.value as Verbosity | '')}>
   {accountDefault && <option value="">Account default</option>}
   <option value="narrated">Narrated</option><option value="standard">Standard</option><option value="terse">Terse</option>
  </select>
 </label>
}

function SavedVerbosity({ id, initial }: { id?: string; initial: Verbosity }) {
 const [value, setValue] = useState(initial)
 const [busy, setBusy] = useState(!id)
 const [error, setError] = useState('')
 const [notice, setNotice] = useState('')
 const [account, setAccount] = useState(signedInAccount)
 const epoch = useRef(0)
 useEffect(() => {
  const invalidate = () => { epoch.current++ }
  const changed = () => { invalidate(); setAccount(signedInAccount()); setValue('standard'); setNotice(''); setError('') }
  window.addEventListener('blerg-identity-changed', changed)
  return () => { invalidate(); window.removeEventListener('blerg-identity-changed', changed) }
 }, [])
 const [previousInitial, setPreviousInitial] = useState(initial)
 if (previousInitial !== initial) { setPreviousInitial(initial); setValue(initial) }
 useEffect(() => {
  if (id) return
  const controller = new AbortController()
  void (async () => {
   setBusy(true)
   try {
    const response = await apiFetch('/api/me/preferences', {signal:controller.signal})
    if (!response.ok) throw new Error()
    const body = await response.json() as {verbosity?: Verbosity}
    if (!controller.signal.aborted) setValue(body.verbosity ?? 'standard')
   } catch { if (!controller.signal.aborted) setError('Could not load verbosity. Reopen Settings to retry.') }
   finally { if (!controller.signal.aborted) setBusy(false) }
  })()
  return () => controller.abort()
 }, [id, account])
 async function save(next: Verbosity | '') {
  if (!next || account !== signedInAccount()) return
  const generation = epoch.current
  setBusy(true); setError(''); setNotice('')
  try {
   const response = await apiFetch(id ? `/api/sessions/${id}/verbosity` : '/api/me/preferences', {
    method:id ? 'POST' : 'PUT', headers:{'Content-Type':'application/json'}, body:JSON.stringify({verbosity:next}),
   })
   if (!response.ok) {
    const body = await response.json().catch(() => ({})) as {error?:string}
    throw new Error(body.error || 'Could not save verbosity. Try again.')
   }
   if (generation !== epoch.current) return
   setValue(next); setNotice(id ? 'Applies on the next turn.' : 'Saved for new sessions. Children default to Terse.')
  } catch (err) { if (generation === epoch.current) setError(err instanceof Error ? err.message : 'Could not save verbosity. Try again.') }
  finally { if (generation === epoch.current) setBusy(false) }
 }
 return <div style={{ maxWidth:'100%' }}>
  <VerbositySelect label={id ? 'Verbosity' : 'Default verbosity'} value={value} disabled={busy} onChange={next => void save(next)} />
  {error && <p role="alert">{error}</p>}{notice && <p role="status" style={{fontSize:12,whiteSpace:'normal'}}>{notice}</p>}
 </div>
}
export function VerbositySettings() {
 const [open, setOpen] = useState(false)
 return <details onToggle={event => setOpen(event.currentTarget.open)} className="settings-section st-section st-verbosity"><summary className="settings-heading">Session verbosity</summary>
  <p className="settings-description">Narrated explains progress, Standard gives milestone updates, and Terse keeps replies brief. This is separate from unattended mode.</p>
  {open && <SavedVerbosity initial="standard" />}
 </details>
}
export function SessionVerbosity({id, value}: {id:string;value:Verbosity}) {
 return <SavedVerbosity key={id} id={id} initial={value} />
}
