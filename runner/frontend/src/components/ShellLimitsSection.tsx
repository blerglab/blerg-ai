// Settings → Shells (administrators): how long an owner shell may have no browser attached, and
// how long it may go without typing, for desktop and cluster shells, install-wide. Each limit is
// the runtime default, "until the session ends", or a duration from 10 minutes to 87,600 hours. Hidden for everyone else.
import { useEffect, useId, useState } from 'react'
import { apiFetch } from '../apiFetch'
import { shellLimitText } from '../shellTransport'

type Pair = { detached_seconds: number; idle_seconds: number }
type Stored = { detached_seconds: number | null; idle_seconds: number | null }
type Reply = { desktop: Pair; cluster: Pair; defaults: { desktop: Pair; cluster: Pair }; stored: { desktop: Stored; cluster: Stored }; min_seconds: number; max_seconds: number }
type Runtime = 'desktop' | 'cluster'
type Limit = 'detached_seconds' | 'idle_seconds'
// A draft remembers whether the person edited it: an untouched limit is never sent, so a stored
// value that is not a whole number of minutes survives a save of another limit exactly.
type Draft = { mode: 'default' | 'session' | 'after'; amount: string; unit: 'minutes' | 'hours'; edited: boolean }

const RUNTIMES: { id: Runtime; label: string }[] = [{ id: 'desktop', label: 'Desktop' }, { id: 'cluster', label: 'Cluster' }]
const LIMITS: { id: Limit; label: string }[] = [
  { id: 'detached_seconds', label: 'Close a shell with no browser attached after' },
  { id: 'idle_seconds', label: 'Close a shell with no typing after' },
]
const key = (r: Runtime, l: Limit) => `${r}.${l}`

function isReply(v: unknown): v is Reply {
  const pair = (p: unknown) => !!p && typeof p === 'object' && typeof (p as Pair).detached_seconds === 'number' && typeof (p as Pair).idle_seconds === 'number'
  const stored = (p: unknown) => !!p && typeof p === 'object' && [(p as Stored).detached_seconds, (p as Stored).idle_seconds].every(x => x === null || typeof x === 'number')
  const r = v as Reply
  return !!r && pair(r.desktop) && pair(r.cluster) && !!r.defaults && pair(r.defaults.desktop) && pair(r.defaults.cluster) && !!r.stored && stored(r.stored.desktop) && stored(r.stored.cluster) && typeof r.min_seconds === 'number' && typeof r.max_seconds === 'number'
}
function amountOf(seconds: number): Pick<Draft, 'amount' | 'unit'> {
  if (seconds <= 0) return { amount: '1', unit: 'hours' }
  if (seconds % 3600 === 0) return { amount: String(seconds / 3600), unit: 'hours' }
  return { amount: String(Math.round(seconds / 60)), unit: 'minutes' }
}
function draftOf(stored: number | null, effective: number): Draft {
  if (stored === null) return { mode: 'default', ...amountOf(effective), edited: false }
  if (stored === 0) return { mode: 'session', ...amountOf(0), edited: false }
  return { mode: 'after', ...amountOf(stored), edited: false }
}
function draftsOf(reply: Reply): Record<string, Draft> {
  return Object.fromEntries(RUNTIMES.flatMap(rt => LIMITS.map(l => [key(rt.id, l.id), draftOf(reply.stored[rt.id][l.id], reply[rt.id][l.id])])))
}
// What an edited draft stores: null for the default, 0 for until the session ends, or seconds;
// undefined while the amount is not a whole positive number.
function valueOf(d: Draft): number | null | undefined {
  if (d.mode === 'default') return null
  if (d.mode === 'session') return 0
  if (!/^\d+$/.test(d.amount.trim())) return undefined
  const n = Number(d.amount.trim())
  return n > 0 ? n * (d.unit === 'hours' ? 3600 : 60) : undefined
}
const describe = (seconds: number) => seconds === 0 ? 'until the session ends' : shellLimitText(seconds)

export default function ShellLimitsSection() {
  const headingId = useId()
  const [reply, setReply] = useState<Reply | null>(null)
  const [hidden, setHidden] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState('')
  const [busy, setBusy] = useState(false)
  const [drafts, setDrafts] = useState<Record<string, Draft>>({})
  const [reload, setReload] = useState(0)

  useEffect(() => {
    let active = true
    apiFetch('/api/shells/settings').then(async r => {
      if (r.status === 401 || r.status === 403) { if (active) setHidden(true); return }
      const body: unknown = r.ok ? await r.json() : null
      if (!isReply(body)) throw new Error()
      if (!active) return
      setReply(body); setError(''); setDrafts(draftsOf(body))
    }).catch(() => { if (active) setError('Could not load the shell limits. Try again.') })
    return () => { active = false }
  }, [reload])

  if (hidden) return null
  const problem = (r: Runtime, l: Limit): string => {
    const d = drafts[key(r, l)]
    if (!d || !reply || !d.edited || d.mode !== 'after') return ''
    const s = valueOf(d)
    if (s === undefined || s === null) return 'Enter a whole number.'
    if (s < reply.min_seconds) return `The shortest limit is ${shellLimitText(reply.min_seconds)}.`
    if (s > reply.max_seconds) return `The longest limit is ${(reply.max_seconds / 3600).toLocaleString('en-US')} hours.`
    return ''
  }
  const changes: Partial<Record<Runtime, Partial<Stored>>> = {}
  let invalid = false
  if (reply) for (const rt of RUNTIMES) for (const l of LIMITS) {
    const d = drafts[key(rt.id, l.id)]
    if (!d?.edited) continue
    if (problem(rt.id, l.id)) { invalid = true; continue }
    const v = valueOf(d)
    if (v !== undefined && v !== reply.stored[rt.id][l.id]) changes[rt.id] = { ...changes[rt.id], [l.id]: v }
  }
  const dirty = Object.keys(changes).length > 0
  const set = (r: Runtime, l: Limit, patch: Partial<Draft>) => { setSaved(''); setDrafts(ds => ({ ...ds, [key(r, l)]: { ...ds[key(r, l)], ...patch, edited: true } })) }

  async function save() {
    setBusy(true); setError(''); setSaved('')
    try {
      const r = await apiFetch('/api/shells/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(changes) })
      const body: unknown = await r.json().catch(() => null)
      if (!r.ok || !isReply(body)) {
        const message = body && typeof body === 'object' && typeof (body as { error?: unknown }).error === 'string' ? (body as { error: string }).error : ''
        throw new Error(r.status === 403 ? 'Only an administrator can change this.' : message || 'Could not save the shell limits. Try again.')
      }
      setReply(body); setDrafts(draftsOf(body))
      setSaved('Saved. Running shells use the new limits.')
    } catch (e) { setError(e instanceof Error ? e.message : 'Could not save the shell limits. Try again.') }
    finally { setBusy(false) }
  }

  return <section className="settings-section st-section" aria-labelledby={headingId} data-testid="shell-limits">
    <h3 id={headingId} className="settings-heading st-heading">Shells</h3>
    <p className="settings-description">Install-wide limits for shells in sessions. Only administrators see and change these.</p>
    {error && <p role="alert" className="st-note">{error} {!reply && <button type="button" className="settings-action btn small" onClick={() => setReload(n => n + 1)}>Retry</button>}</p>}
    {!reply && !error && <p className="st-note">Loading shell limits…</p>}
    {reply && <>
      <div className="st-limits">
        {LIMITS.map(l => <fieldset key={l.id} className="st-limit-group">
          <legend className="st-limit-legend">{l.label}…</legend>
          {RUNTIMES.map(rt => {
            const d = drafts[key(rt.id, l.id)] ?? draftOf(reply.stored[rt.id][l.id], reply[rt.id][l.id])
            const name = `${rt.label}: ${l.label.toLowerCase()}`
            const issue = problem(rt.id, l.id)
            const fallback = reply.defaults[rt.id][l.id]
            return <div key={rt.id} className="st-limit" data-testid={`shell-limit-${rt.id}-${l.id === 'detached_seconds' ? 'detached' : 'idle'}`}>
              <span className="st-limit-runtime">{rt.label}</span>
              <select className="settings-field select" aria-label={name} value={d.mode} onChange={e => set(rt.id, l.id, { mode: e.target.value as Draft['mode'] })}>
                <option value="default">Default ({describe(fallback)})</option>
                <option value="session">Until the session ends</option>
                <option value="after">After a set time</option>
              </select>
              {d.mode === 'after' && <span className="st-limit-amount">
                <input className="settings-field st-limit-number" type="number" min={1} step={1} inputMode="numeric" aria-label={`${name}: amount`} aria-invalid={!!issue} value={d.amount} onChange={e => set(rt.id, l.id, { amount: e.target.value })} />
                <select className="settings-field select" aria-label={`${name}: unit`} value={d.unit} onChange={e => set(rt.id, l.id, { unit: e.target.value as Draft['unit'] })}>
                  <option value="minutes">minutes</option>
                  <option value="hours">hours</option>
                </select>
              </span>}
              {issue && <span className="st-limit-error" role="alert">{issue}</span>}
            </div>
          })}
        </fieldset>)}
      </div>
      <p className="st-hint st-limit-hint">Desktop covers shells on a desktop, sandboxed or not; cluster covers shells in cluster session pods. A change applies to running shells too, and a shell whose limit gets shorter keeps at least {shellLimitText(reply.min_seconds)}. For now, a desktop shell still ends when the computer sleeps or loses its network connection.</p>
      <div className="st-limit-actions">
        {saved && <span className="st-note" role="status">{saved}</span>}
        <button type="button" className="settings-action btn primary" disabled={!dirty || invalid || busy} onClick={() => void save()}>{busy ? 'Saving…' : 'Save shell limits'}</button>
      </div>
    </>}
  </section>
}
