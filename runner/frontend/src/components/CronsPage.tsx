// CronsPage (/crons): the person's scheduled agent runs (spec 7.7). Each row shows the schedule in
// words, the next and last run, its status, an enabled switch (a plain toggle that mints and revokes
// nothing), Resume for a paused cron (a pause revoked its access token; resuming issues a new one),
// the access token's expiry with Renew, Run now, Edit, Delete (confirmed in the page) and the run
// history.
import { useCallback, useEffect, useState } from 'react'
import type { CSSProperties } from 'react'
import { apiFetch } from '../apiFetch'
import { describeSchedule } from '../lib/cronSchedule'
import type { CronInfo, CronRunInfo } from '../types'
import CronForm from './CronForm'
import CronRuns from './CronRuns'

const button: CSSProperties = {
  background: 'var(--scree)', border: '1px solid var(--stone)', borderRadius: 6, color: 'var(--fog)',
  padding: '4px 10px', cursor: 'pointer', fontSize: '0.8rem', fontFamily: 'inherit',
}
const primary: CSSProperties = {
  ...button, background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))', border: '1px solid var(--blaze)', color: 'var(--amber)', fontWeight: 700,
}
const TOKEN_WARN_MS = 30 * 24 * 60 * 60 * 1000

const PILL: Record<string, { color: string }> = {
  active: { color: 'var(--success, #4caf50)' },
  disabled: { color: 'var(--fog-dim)' },
  paused: { color: 'var(--amber)' },
  expired: { color: 'var(--danger)' },
}

function when(iso: string | null, timeZone?: string): string {
  if (!iso) return 'never'
  const d = new Date(iso)
  try {
    return d.toLocaleString(undefined, { timeZone, dateStyle: 'medium', timeStyle: 'short' })
  } catch {
    return d.toLocaleString()
  }
}

async function errorText(res: Response): Promise<string> {
  try {
    const body = await res.json() as { error?: string }
    if (body.error) return body.error
  } catch { /* fall through */ }
  return `HTTP ${res.status}`
}

function lastRunText(c: CronInfo): string {
  if (!c.last_run) return 'never'
  const r = c.last_run
  const bits = [when(r.scheduled_for, c.timezone), r.status]
  if (r.late) bits.push('ran late')
  if (r.reason) bits.push(r.reason)
  return bits.join(' · ')
}

export default function CronsPage() {
  const [crons, setCrons] = useState<CronInfo[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [editing, setEditing] = useState<CronInfo | 'new' | null>(null)
  const [confirming, setConfirming] = useState<string | null>(null)
  const [history, setHistory] = useState<Set<string>>(new Set())
  const [historyKey, setHistoryKey] = useState(0)
  const [busy, setBusy] = useState<string | null>(null)
  // The clock as of the last load, for the token-expiry warning (render stays pure).
  const [loadedAt, setLoadedAt] = useState(0)

  const load = useCallback(async () => {
    try {
      const res = await apiFetch('/api/crons')
      if (!res.ok) throw new Error(await errorText(res))
      const body = await res.json() as { crons?: CronInfo[] }
      setCrons(body.crons ?? [])
      setLoadedAt(Date.now())
      setError(null)
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e))
      setCrons(prev => prev ?? [])
    }
  }, [])

  useEffect(() => {
    // The initial fetch: it sets state only after it resolves.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load()
  }, [load])

  async function act(id: string, path: string, method: string, then?: (res: Response) => Promise<void>, body?: unknown) {
    setBusy(id)
    setError(null)
    setNotice(null)
    try {
      const res = await apiFetch(`/api/crons/${encodeURIComponent(id)}${path}`, body === undefined ? { method } : {
        method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
      })
      if (!res.ok) {
        setError(await errorText(res))
        return
      }
      if (then) await then(res)
      setHistoryKey(k => k + 1)
      await load()
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(null)
    }
  }

  const runNow = (c: CronInfo) => act(c.id, '/run', 'POST', async res => {
    const run = await res.json() as CronRunInfo
    setNotice(run.status === 'started' ? `${c.name}: a run has started.`
      : `${c.name}: the run is ${run.status}${run.reason ? ` (${run.reason})` : ''}.`)
  })

  if (editing) {
    return (
      <CronForm
        cron={editing === 'new' ? undefined : editing}
        onCancel={() => setEditing(null)}
        onSaved={() => { setEditing(null); void load() }}
      />
    )
  }

  return (
    <div style={{ padding: 16, color: 'var(--chalk)', maxWidth: 880 }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 14 }}>
        <span style={{ color: 'var(--blaze)', fontWeight: 700, fontSize: '1rem', letterSpacing: '0.1em', textTransform: 'uppercase' }}>Crons</span>
        <button type="button" onClick={() => setEditing('new')} style={primary}>New cron</button>
      </div>

      {error && <p role="alert" data-testid="crons-error" style={{ color: 'var(--danger)', fontSize: '0.85rem' }}>{error}</p>}
      {notice && <p role="status" data-testid="cron-notice" style={{ color: 'var(--fog)', fontSize: '0.85rem' }}>{notice}</p>}
      {crons === null && !error && <p style={{ color: 'var(--fog-dim)' }}>Loading…</p>}
      {crons?.length === 0 && !error && (
        <p data-testid="crons-empty" style={{ color: 'var(--fog)' }}>No crons yet. A cron runs an agent prompt on a schedule, with only the tools you give it.</p>
      )}

      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        {crons?.map(c => {
          const paused = c.paused_reason !== null
          const expiresMs = new Date(c.token_expires_at).getTime() - loadedAt
          const tokenExpired = c.status === 'expired' || expiresMs <= 0
          const tokenWarn = tokenExpired || expiresMs < TOKEN_WARN_MS
          const pill = PILL[c.status] ?? PILL.active
          return (
            <div key={c.id} data-testid={`cron-${c.id}`} style={{ background: 'var(--scree)', border: '1px solid var(--stone)', borderRadius: 8, padding: '10px 14px' }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
                <span style={{ fontWeight: 700 }}>{c.name}</span>
                <span data-testid={`cron-status-${c.id}`} style={{ fontSize: '0.7rem', border: `1px solid ${pill.color}`, color: pill.color, borderRadius: 10, padding: '1px 8px' }}>{c.status}</span>
                {paused ? (
                  // A pause revoked the access token, so getting out of it issues a new one: an
                  // explicit action of its own, not a switch.
                  <button
                    type="button" aria-label={`Resume: ${c.name}`} title="Issues a new access token and schedules the next run"
                    disabled={busy === c.id} onClick={() => act(c.id, '/resume', 'POST')}
                    style={{ ...button, marginLeft: 'auto', color: 'var(--amber)' }}
                  >
                    Resume
                  </button>
                ) : (
                  // A plain toggle: it neither revokes nor mints a token.
                  <button
                    type="button" role="switch" aria-checked={c.enabled} aria-label={`Enabled: ${c.name}`} disabled={busy === c.id}
                    onClick={() => act(c.id, '', 'PATCH', undefined, { enabled: !c.enabled })}
                    style={{ ...button, marginLeft: 'auto', color: c.enabled ? 'var(--amber)' : 'var(--fog-dim)' }}
                  >
                    {c.enabled ? 'Enabled' : 'Disabled'}
                  </button>
                )}
              </div>
              <div style={{ color: 'var(--fog)', fontSize: '0.85rem', marginTop: 4 }}>{describeSchedule(c.schedule)} ({c.timezone})</div>
              {paused && (
                <div data-testid={`cron-paused-reason-${c.id}`} style={{ color: 'var(--amber)', fontSize: '0.8rem', marginTop: 4 }}>
                  Paused: {c.paused_reason}
                </div>
              )}
              <div style={{ color: 'var(--fog-dim)', fontSize: '0.8rem', marginTop: 4, display: 'flex', gap: 16, flexWrap: 'wrap' }}>
                <span>Next run: {paused || !c.enabled ? 'not scheduled' : when(c.next_run_at, c.timezone)}</span>
                <span>Last run: {lastRunText(c)}</span>
              </div>
              <div data-testid={`cron-token-${c.id}`} style={{ fontSize: '0.8rem', marginTop: 4, display: 'flex', alignItems: 'center', gap: 8, color: tokenWarn ? 'var(--amber)' : 'var(--fog-dim)' }}>
                <span>{tokenExpired ? 'Access token expired' : `Access expires ${when(c.token_expires_at)}`}</span>
                <button type="button" onClick={() => act(c.id, '/renew', 'POST')} disabled={busy === c.id || paused} title={paused ? 'Resume the cron to get a fresh token' : undefined} style={button}>Renew</button>
              </div>
              {c.connection_problems.length > 0 && (
                <ul data-testid={`cron-problems-${c.id}`} style={{ color: 'var(--danger)', fontSize: '0.8rem', margin: '4px 0 0', paddingLeft: 18 }}>
                  {c.connection_problems.map(p => <li key={p}>{p}</li>)}
                </ul>
              )}
              <div style={{ display: 'flex', gap: 6, marginTop: 8, flexWrap: 'wrap' }}>
                {!paused && <button type="button" onClick={() => runNow(c)} disabled={busy === c.id} style={button}>Run now</button>}
                <button type="button" onClick={() => setEditing(c)} style={button}>Edit</button>
                <button
                  type="button" aria-expanded={history.has(c.id)}
                  onClick={() => setHistory(prev => { const n = new Set(prev); if (n.has(c.id)) n.delete(c.id); else n.add(c.id); return n })}
                  style={button}
                >
                  History
                </button>
                <button type="button" onClick={() => setConfirming(c.id)} style={{ ...button, color: 'var(--danger)' }}>Delete</button>
              </div>
              {confirming === c.id && (
                <div data-testid={`cron-delete-confirm-${c.id}`} style={{ marginTop: 8, border: '1px solid var(--danger)', borderRadius: 6, padding: '8px 10px', fontSize: '0.85rem' }}>
                  <p style={{ margin: '0 0 8px' }}>Delete "{c.name}"? This revokes its access token, stops a run in progress and removes its history. It cannot be undone.</p>
                  <div style={{ display: 'flex', gap: 6 }}>
                    <button
                      type="button" style={{ ...button, color: 'var(--danger)', borderColor: 'var(--danger)' }}
                      onClick={() => { setConfirming(null); void act(c.id, '', 'DELETE') }}
                    >
                      Confirm delete
                    </button>
                    <button type="button" style={button} onClick={() => setConfirming(null)}>Cancel</button>
                  </div>
                </div>
              )}
              {history.has(c.id) && <CronRuns cronId={c.id} refreshKey={historyKey} />}
            </div>
          )
        })}
      </div>
    </div>
  )
}
