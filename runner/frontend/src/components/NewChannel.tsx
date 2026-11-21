import { IconButton } from '@blerglab/chat'
import { useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useToastStore } from '../hooks/useToastStore'
import { apiFetch } from '../apiFetch'
import { useSessionStore } from '../hooks/useSessionStore'
import { useLaunchOptions } from '../lib/launchOptions'
import { repoLabel } from '../lib/noRepo'
import type { SessionInfo } from '../types'

export default function NewChannel({ workspace, close }: { workspace: SessionInfo; close(restoreFocus?: boolean): void }) {
  const navigate = useNavigate()
  const attempt = useRef<{ body: string; key: string } | null>(null)
  const [name, setName] = useState('')
  const [engine, setEngine] = useState('')
  const [inherit, setInherit] = useState(true)
  const [privateChat, setPrivate] = useState(false)
  const members = workspace.workspace?.members ?? [{ id: workspace.repo, name: repoLabel(workspace.repo), path: '' }]
  const [repos, setRepos] = useState(members.map(m => m.id ?? m.name))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const options = useLaunchOptions({ runtime: workspace.runtime ?? 'daemon', daemonId: workspace.daemon_id })
  const selectedEngine = options.engines.find(e => e.id === engine)?.id || options.engines.find(e => e.id === workspace.engine)?.id || options.engines[0]?.id || ''
  async function start(event: React.FormEvent) {
    event.preventDefault()
    if (busy) return
    setBusy(true); setError('')
    try {
      const payload = JSON.stringify({ title: name.trim(), engine: selectedEngine, repos, inherit_permissions: inherit, private: privateChat })
      if (attempt.current?.body !== payload) attempt.current = { body: payload, key: crypto.randomUUID() }
      const response = await apiFetch(`/api/sessions/${encodeURIComponent(workspace.id)}/channels`, {
        method: 'POST', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': attempt.current.key },
        body: payload,
      })
      const body = await response.json().catch(() => null)
      if (!response.ok || !body || typeof body !== 'object') throw new Error(typeof body?.error === 'string' ? body.error : 'Could not start the channel.')
      if (typeof body.session_id !== 'string' || !body.session_id) throw new Error('The channel response is missing its session ID.')
      if (Array.isArray(body.permission_problems) && body.permission_problems.length) useToastStore.getState().addToast({ title: 'Some permissions were not given', body: body.permission_problems.join(' '), url: `/sessions/${body.session_id}` })
      useSessionStore.getState().addPendingSession(body.session_id)
      close(false); navigate(`/sessions/${encodeURIComponent(body.session_id)}`)
    } catch (err) { setError(err instanceof Error ? err.message : 'Could not start the channel.'); setBusy(false) }
  }
  return <form className="dock-new-channel" aria-label="New channel" onSubmit={event => { void start(event) }} onKeyDown={event => { if (event.key === 'Escape' && !busy) { event.stopPropagation(); close() } }}>
    <label>Channel name<input autoFocus required maxLength={60} value={name} onChange={e => setName(e.target.value)} disabled={busy} /></label>
    <label>Engine<select value={selectedEngine} onChange={e => setEngine(e.target.value)} disabled={busy || options.state !== 'known'}>{options.engines.length === 0 && <option value="">{options.state === 'loading' ? 'Loading engines…' : 'No engines available'}</option>}{options.engines.map(e => <option key={e.id} value={e.id}>{e.name}</option>)}</select></label>
    {options.error && <p role="alert">{options.error} <IconButton label="Retry" onClick={options.retry} /></p>}
    <details><summary>Change repositories and permissions</summary>
      <fieldset disabled={busy}><legend>Repositories</legend>{members.map(m => { const id = m.id ?? m.name; return <label className="dock-channel-option" key={id}><input type="checkbox" checked={repos.includes(id)} onChange={e => setRepos(previous => e.target.checked ? [...previous, id] : previous.filter(v => v !== id))} />{m.name}</label> })}</fieldset>
      <label className="dock-channel-option"><input type="checkbox" checked={inherit} onChange={e => setInherit(e.target.checked)} disabled={busy} />Inherit workspace permissions</label>
      <label className="dock-channel-option"><input type="checkbox" checked={privateChat} onChange={e => setPrivate(e.target.checked)} disabled={busy} />Private chat</label>
      <p>Private chats are visible to you. Main can see and organize the channel, but cannot read it. Channels in private workspaces stay private.</p>
    </details>
    {error && <p role="alert">{error}</p>}
    <div><button className="btn primary small" disabled={busy || !name.trim() || !selectedEngine || repos.length === 0}>Start channel</button> <button type="button" className="btn small" disabled={busy} onClick={() => close()}>Cancel</button></div>
  </form>
}
