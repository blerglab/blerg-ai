// CronRuns: a cron's run history (GET /api/crons/{id}/runs), newest slot first. A run that started
// long after its slot (the machine was off, a daemon was away) says "ran late"; a run that did not
// start says why; a run that started links to its session.
import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { apiFetch } from '../apiFetch'
import type { CronRunInfo } from '../types'

const chip: React.CSSProperties = {
  fontSize: '0.68rem',
  borderRadius: 4,
  padding: '1px 6px',
  border: '1px solid var(--stone)',
  color: 'var(--fog)',
  background: 'var(--basalt)',
}

const STATUS_COLOR: Record<string, string> = {
  started: 'var(--success, #4caf50)',
  held: 'var(--amber)',
  claimed: 'var(--amber)',
  skipped: 'var(--fog-dim)',
  failed: 'var(--danger)',
}

async function errorText(res: Response): Promise<string> {
  try {
    const body = await res.json() as { error?: string }
    if (body.error) return body.error
  } catch { /* fall through */ }
  return `HTTP ${res.status}`
}

export default function CronRuns({ cronId, refreshKey = 0 }: { cronId: string; refreshKey?: number }) {
  const [runs, setRuns] = useState<CronRunInfo[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let live = true
    apiFetch(`/api/crons/${encodeURIComponent(cronId)}/runs`)
      .then(async res => {
        if (!res.ok) throw new Error(await errorText(res))
        const body = await res.json() as { runs?: CronRunInfo[] }
        if (live) { setRuns(body.runs ?? []); setError(null) }
      })
      .catch((e: unknown) => { if (live) setError(e instanceof Error ? e.message : String(e)) })
    return () => { live = false }
  }, [cronId, refreshKey])

  if (error) return <p role="alert" style={{ color: 'var(--danger)', fontSize: '0.8rem', margin: '6px 0' }}>{error}</p>
  if (runs === null) return <p style={{ color: 'var(--fog-dim)', fontSize: '0.8rem', margin: '6px 0' }}>Loading runs…</p>
  if (runs.length === 0) {
    return <p data-testid="cron-runs-empty" style={{ color: 'var(--fog-dim)', fontSize: '0.8rem', margin: '6px 0' }}>No runs yet.</p>
  }
  return (
    <ul data-testid="cron-runs" style={{ listStyle: 'none', margin: '6px 0 0', padding: 0, display: 'flex', flexDirection: 'column', gap: 4 }}>
      {runs.map(r => (
        <li
          key={r.id}
          data-testid={`cron-run-${r.id}`}
          style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8, fontSize: '0.8rem', color: 'var(--chalk)', padding: '4px 0', borderTop: '1px solid var(--stone)' }}
        >
          <span style={{ fontFamily: 'monospace', color: 'var(--fog)' }}>{new Date(r.scheduled_for).toLocaleString()}</span>
          <span style={{ color: STATUS_COLOR[r.status] ?? 'var(--fog)', fontWeight: 600 }}>{r.status}</span>
          {r.late && <span style={{ ...chip, color: 'var(--amber)', borderColor: 'var(--amber)' }} title="It started long after its scheduled time">ran late</span>}
          {r.manual && <span style={chip}>manual</span>}
          {r.reason && <span style={{ color: 'var(--fog)' }}>{r.reason}</span>}
          {r.session_id && (
            <Link to={`/sessions/${r.session_id}`} style={{ color: 'var(--amber)', marginLeft: 'auto' }}>Open session</Link>
          )}
        </li>
      ))}
    </ul>
  )
}
