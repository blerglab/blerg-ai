import { useEffect, useId, useState } from 'react'
import { apiFetch } from '../apiFetch'
import { signedInAccount } from '../lib/liveBoardCard'

type Switches = Partial<Record<'test-driven-development' | 'ux-design' | 'visualizing', boolean>>
type Settings = { install: Switches; account: Switches; workspace: Switches; effective: Switches; admin: boolean; workspaces: { id: string; title: string }[] }
const names = ['test-driven-development', 'ux-design', 'visualizing'] as const
const labels = { 'test-driven-development': 'Test-driven development', 'ux-design': 'UX design', visualizing: 'Charts and diagrams' }

// Lives in the existing Settings dialog, alongside the other account/install settings.
export default function SkillSettingsSection() {
  const id = useId()
  const [account, setAccount] = useState(signedInAccount)
  const [open, setOpen] = useState(false)
  const [scope, setScope] = useState('account')
  const [data, setData] = useState<Settings | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  useEffect(() => {
    const changed = () => { setAccount(signedInAccount()); setScope('account'); setData(null); setBusy(false); setError(''); setNotice('') }
    window.addEventListener('blerg-identity-changed', changed)
    return () => window.removeEventListener('blerg-identity-changed', changed)
  }, [])
  const workspace = scope !== 'account' && scope !== 'install' ? scope : ''
  const path = '/api/skills/settings' + (workspace ? `?workspace=${encodeURIComponent(workspace)}` : '')
  useEffect(() => {
    if (!open) return
    const controller = new AbortController()
    void apiFetch(path, { signal: controller.signal }).then(async response => {
      if (!response.ok) throw new Error('Could not load skills.')
      const next = await response.json() as Settings
      if (!controller.signal.aborted && account === signedInAccount()) { setData(next); setError('') }
    }).catch(() => { if (!controller.signal.aborted) setError('Could not load skills. Close this section and try again.') })
    return () => controller.abort()
  }, [open, path, account, scope])
  async function save(name: typeof names[number], enabled: boolean) {
    setBusy(true); setError(''); setNotice('')
    try {
      const response = await apiFetch(path, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ scope: workspace ? 'workspace' : scope, skills: { [name]: enabled } }) })
      if (!response.ok) throw new Error('Could not save skills.')
      const next = await response.json() as Settings
      if (account !== signedInAccount()) return
      setData(next)
      setNotice('Saved. Applies to new sessions; existing sessions keep their applied skills.')
    } catch { if (account === signedInAccount()) setError('Could not save skills.') }
    finally { if (account === signedInAccount()) setBusy(false) }
  }
  const stored = scope === 'install' ? data?.install : workspace ? data?.workspace : data?.account
  return <details className="settings-section st-section" onToggle={event => setOpen(event.currentTarget.open)}>
    <summary className="settings-heading">Skills</summary>
    <p className="settings-description">TDD, UX and charts and diagrams default on. An install or account disable also applies to its workspaces.</p>
    <label className="settings-field" htmlFor={id}>Apply to </label>
    <select id={id} className="settings-field select skill-scope" value={scope} disabled={!data || busy} onChange={event => { setScope(event.target.value); setData(null); setNotice('') }}>
      <option value="account">My account</option>
      {data?.admin && <option value="install">This install</option>}
      {scope === 'install' && !data && <option value="install">This install</option>}
      {data?.workspaces.map(item => <option key={item.id} value={item.id}>{item.title}</option>)}
      {workspace && !data && <option value={workspace}>Workspace</option>}
    </select>
    {names.map(name => {
      const adminOff = scope !== 'install' && data?.install[name] === false
      const accountOff = !!workspace && data?.account[name] === false
      const reason = adminOff ? 'Disabled by administrator' : accountOff ? 'Disabled for your account' : ''
      return <div key={name}>
        <label className="settings-field st-check">
          <input className="settings-toggle-control" type="checkbox" checked={!reason && stored?.[name] !== false} disabled={!data || busy || !!reason} onChange={event => void save(name, event.target.checked)} />
          <span>{labels[name]}</span>
        </label>
        {reason && <p className="st-hint">{reason}</p>}
      </div>
    })}
    <p className="st-hint" title="Always applied to Main. Workers and plain channels do not load it.">Coordinating · always on for Main</p>
    {error && <p className="cc-error" role="alert">{error}</p>}
    {notice && <p className="st-hint" role="status">{notice}</p>}
  </details>
}
