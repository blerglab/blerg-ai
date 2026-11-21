import { useEffect, useId, useRef, useState } from 'react'
import { apiFetch } from '../apiFetch'
import { signedInAccount } from '../lib/liveBoardCard'

export default function BranchSettingsSection() {
  const id = useId()
  const [account, setAccount] = useState(signedInAccount)
  const currentAccount = useRef(account)
  const [open, setOpen] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const [template, setTemplate] = useState('')
  const [saved, setSaved] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  useEffect(() => {
    const changed = () => {
      const next = signedInAccount()
      if (next === currentAccount.current) return
      currentAccount.current = next
      setAccount(next); setLoaded(false); setBusy(false)
      setTemplate(''); setSaved(''); setError(''); setNotice('')
    }
    window.addEventListener('blerg-identity-changed', changed)
    return () => window.removeEventListener('blerg-identity-changed', changed)
  }, [])
  useEffect(() => {
    if (!open || loaded) return
    const controller = new AbortController()
    void (async () => {
      try {
        const response = await apiFetch('/api/me/preferences', { signal: controller.signal })
        if (!response.ok) throw new Error('Could not load the branch template.')
        const preferences = await response.json() as { branch_template?: string }
        if (!controller.signal.aborted && account === currentAccount.current) {
          const value = preferences.branch_template || ''
          setTemplate(value); setSaved(value); setLoaded(true); setError('')
        }
      } catch { if (!controller.signal.aborted && account === currentAccount.current) setError('Could not load the branch template. Close this section and try again.') }
    })()
    return () => controller.abort()
  }, [open, loaded, account])
  async function save() {
    if (account !== signedInAccount()) return
    const savingAccount = account
    setBusy(true); setError(''); setNotice('')
    try {
      const response = await apiFetch('/api/me/preferences', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ branch_template: template }) })
      if (!response.ok) {
        const body = await response.json() as { error?: string }
        throw new Error(body.error || 'Could not save the branch template.')
      }
      if (savingAccount === currentAccount.current) { setSaved(template); setNotice('Saved. New workspaces will use this template.') }
    } catch (err) { if (savingAccount === currentAccount.current) setError(err instanceof Error ? err.message : 'Could not save the branch template.') }
    finally { if (savingAccount === currentAccount.current) setBusy(false) }
  }
  return <details className="settings-section st-section" onToggle={event => setOpen(event.currentTarget.open)}>
    <summary className="settings-heading">Work branches</summary>
    <p className="settings-description">Name the branches created for your new workspaces. Existing workspaces keep their names.</p>
    <label htmlFor={id}>Branch template</label>
    <input id={id} className="settings-field" style={{ width: '100%', boxSizing: 'border-box' }} value={template} maxLength={180} disabled={!loaded || busy} onChange={event => { setTemplate(event.target.value); setNotice('') }} />
    <p className="st-hint">Leave blank for a title and unique suffix, such as wip/fix-button-7d67a123. Use {'{username}'}, {'{session}'}, {'{slug}'}, {'{date}'}, or {'{card}'}. A missing card is omitted. A suffix keeps branch names unique.</p>
    <button type="button" className="settings-action btn primary" disabled={!loaded || busy || template === saved} onClick={() => void save()}>Save branch template</button>
    {error && <p role="alert">{error}</p>}
    {notice && <p role="status">{notice}</p>}
  </details>
}
