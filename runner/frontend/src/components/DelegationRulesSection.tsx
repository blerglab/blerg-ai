import { useEffect, useState } from 'react'
import { apiFetch } from '../apiFetch'
import type { DaemonApiInfo } from '../lib/launchCache'
import { ENGINE_LABELS } from '../lib/runtimes'

interface Rule { id: string; engine: string; runtime: string; daemon_id: string; daemon_name: string; local_only: boolean; created_at: string }
interface Limits { per_parent_running: number; per_parent_starts: number; per_account_running: number; per_account_starts_per_hour: number }
interface Reply { rules: Rule[]; limits: Limits }
const runtimeName = (runtime: string) => runtime === 'docker' ? 'desktop sandbox' : 'desktop, unsandboxed'

export default function DelegationRulesSection() {
  const [daemons, setDaemons] = useState<DaemonApiInfo[]>([])
  const [reply, setReply] = useState<Reply | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [reload, setReload] = useState(0)
  const [engine, setEngine] = useState('claude')
  const [runtime, setRuntime] = useState('docker')
  const [daemonId, setDaemonId] = useState('')
  const [localPreference, setLocalPreference] = useState<boolean | null>(null)
  // Discover choices from this account's workstation reports, including offline
  // hosts: standing rules are allowed to outlive a sleeping machine.
  const engines = [...new Set(daemons.flatMap(d => d.available_engines ?? []))]
  const effectiveDaemon = daemonId || daemons[0]?.id || ''
  const selectedEngine = engines.includes(engine) ? engine : engines[0] ?? ''
  const localOnly = localPreference ?? ['hermes', 'openclaw'].includes(selectedEngine)
  const effectiveRuntime = selectedEngine === 'openclaw' ? 'daemon' : runtime
  useEffect(() => {
    let active = true
    Promise.all([apiFetch('/api/delegation-rules'), apiFetch('/api/daemons')]).then(async ([rules, hosts]) => {
      if (!rules.ok || !hosts.ok) throw new Error()
      const [data, machines] = await Promise.all([rules.json() as Promise<Reply>, hosts.json() as Promise<DaemonApiInfo[]>])
      if (!Array.isArray(data.rules) || !data.limits) throw new Error()
      if (active) { setReply(data); setDaemons(Array.isArray(machines) ? machines.filter(d => d.mode !== 'runner') : []); setError('') }
    }).catch(() => { if (active) setError('Could not load session-starting rules. Try again.') })
    return () => { active = false }
  }, [reload])
  const mutate = async (url: string, init: RequestInit) => {
    setBusy(true); setError('')
    try {
      const r = await apiFetch(url, init)
      if (!r.ok) throw new Error()
      setReload(n => n + 1)
    } catch { setError('Could not save session-starting rules. Try again.') }
    finally { setBusy(false) }
  }
  return <section className="st-chat" aria-label="Sessions starting sessions" data-testid="delegation-rules">
    <h3 className="st-heading">Sessions starting sessions</h3>
    <p className="st-hint">Standing rules allow any repository on the selected desktop for that engine and runtime. Permissions granted for one session stay limited to the approved repository.</p>
    {error && <p role="alert">{error} <button type="button" onClick={() => setReload(n => n + 1)}>Retry</button></p>}
    {!reply && !error && <p>Loading rules…</p>}
    {reply && <>
      {!reply.rules.length && <p>No standing rules. Sessions ask before starting another session.</p>}
      <ul>{reply.rules.map(r => <li key={r.id}>{ENGINE_LABELS[r.engine] ?? r.engine} · {runtimeName(r.runtime)} · {r.daemon_name || r.daemon_id}{r.local_only ? ' · local only' : ''} · {new Date(r.created_at).toLocaleDateString()} <button type="button" disabled={busy} onClick={() => { void mutate(`/api/delegation-rules/${encodeURIComponent(r.id)}`, { method: 'DELETE' }) }}>Remove</button></li>)}</ul>
      <p className="st-hint">Each session can run {reply.limits.per_parent_running} started sessions at once and start {reply.limits.per_parent_starts}; your account can run {reply.limits.per_account_running} at once and start {reply.limits.per_account_starts_per_hour} an hour.</p>
      {!daemons.length && <p>No workstation daemon is available. Connect one to add a rule.</p>}
      <form onSubmit={e => { e.preventDefault(); if (selectedEngine && effectiveDaemon) void mutate('/api/delegation-rules', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ engine: selectedEngine, runtime: effectiveRuntime, daemon_id: effectiveDaemon, local_only: localOnly }) }) }}>
        <label>Engine <select aria-label="Rule engine" value={selectedEngine} onChange={e => { setEngine(e.target.value); setLocalPreference(null); if (e.target.value === 'openclaw') setRuntime('daemon') }}>{engines.map(id => <option key={id} value={id}>{ENGINE_LABELS[id] ?? id}</option>)}</select></label>{' '}
        <label>Runtime <select aria-label="Rule runtime" value={effectiveRuntime} onChange={e => setRuntime(e.target.value)}><option value="docker" disabled={selectedEngine === 'openclaw'}>desktop sandbox</option><option value="daemon">desktop, unsandboxed</option></select></label>{' '}
        <label>Daemon <select aria-label="Rule daemon" value={effectiveDaemon} onChange={e => setDaemonId(e.target.value)}>{daemons.map(d => <option key={d.id} value={d.id}>{d.name}</option>)}</select></label>
        <label className="st-check"><input type="checkbox" checked={localOnly} onChange={e => setLocalPreference(e.target.checked)} />Local only</label>
        {effectiveRuntime === 'daemon' && <p className="st-hint">Unsandboxed sessions run as you with full filesystem access.</p>}
        <button type="submit" disabled={busy || !selectedEngine || !effectiveDaemon}>Add rule</button>
      </form>
    </>}
  </section>
}
