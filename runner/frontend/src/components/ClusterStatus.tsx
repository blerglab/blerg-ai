import { useState, useEffect, useCallback } from 'react'
import { apiFetch } from '../apiFetch'
import { repoLabel } from '../lib/noRepo'

interface ClusterStatusApi {
  configured: boolean
  namespace?: string
  image?: string
  daemon_id?: string
  // The cap in force (the admin override when set), and the environment value it returns to.
  max_sessions?: number
  max_sessions_default?: number
  active_sessions: number
  available_engines: string[] | null // null when the cluster has no operator credentials
  // Whether the operator Secret carries a shared git token. Without one, a
  // session can only clone public repos unless the launching user has their
  // own GitHub credential.
  git_configured?: boolean
  secret_name?: string
  oauth_secret_name?: string
  cpu_request?: string
  mem_request?: string
  cpu_limit?: string
  mem_limit?: string
  pod_ttl_seconds?: number
  pod_idle_timeout_seconds?: number
  termination_grace_seconds?: number
  ttl_seconds_after_finished?: number
}

interface ClusterSession {
  id: string
  status: string
  repo: string
  title: string
  model?: string
  engine?: string
  started_at: string
}

const ALL_ENGINES = ['claude', 'codex', 'hermes'] as const
const ENGINE_LABELS: Record<string, string> = { claude: 'Claude', codex: 'Codex', hermes: 'Hermes' }

const sectionLabel: React.CSSProperties = {
  color: 'var(--fog)',
  fontSize: '0.7rem',
  fontWeight: 700,
  letterSpacing: '0.1em',
  textTransform: 'uppercase',
  marginBottom: 8,
  display: 'block',
}

const card: React.CSSProperties = {
  background: 'var(--scree)',
  border: '1px solid var(--stone)',
  borderRadius: 8,
  padding: '12px 14px',
  marginBottom: 16,
}

const row: React.CSSProperties = {
  display: 'flex',
  justifyContent: 'space-between',
  padding: '4px 0',
  fontSize: '0.85rem',
  borderBottom: '1px solid color-mix(in srgb, var(--stone) 60%, transparent)',
}

function fmtSeconds(s?: number): string {
  if (!s) return '—'
  if (s % 3600 === 0) return `${s / 3600}h`
  if (s % 60 === 0) return `${s / 60}m`
  return `${s}s`
}

// "500m" -> "0.5 CPU", "1Gi" -> "1 GiB": a pod's resource request as people say it.
function fmtCPU(v: string): string {
  const m = /^(\d+(?:\.\d+)?)m$/.exec(v)
  return `${m ? Number(m[1]) / 1000 : v} CPU`
}
function fmtMem(v: string): string {
  return v.replace(/^(\d+(?:\.\d+)?)(Ki|Mi|Gi|Ti)$/, '$1 $2B')
}

const MIN_SESSIONS = 1
const MAX_SESSIONS = 64

// PodLimits lets an administrator change how long a session pod may sit idle,
// how long any pod may live and how many session pods may run at once.
// Applies to pods started after a save. A non-admin's save is refused by the
// server and the reason is shown.
export function PodLimits({ status, onSaved }: { status: ClusterStatusApi; onSaved: (s: ClusterStatusApi) => void }) {
  const hours = (s?: number) => String((s ?? 0) / 3600)
  const [idle, setIdle] = useState(hours(status.pod_idle_timeout_seconds))
  const [ttl, setTtl] = useState(hours(status.pod_ttl_seconds))
  const shownCap = status.max_sessions == null ? '' : String(status.max_sessions)
  const [cap, setCap] = useState(shownCap)
  // "Use default" was pressed and the cap field not edited since: Save removes the override.
  const [resetCap, setResetCap] = useState(false)
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null)

  const capDefault = status.max_sessions_default
  const capOverridden = capDefault != null && status.max_sessions != null && status.max_sessions !== capDefault

  async function save() {
    setMsg(null)
    // Only a changed cap is sent: 0 removes the override, a number sets it.
    let capChange: number | undefined
    if (resetCap) {
      capChange = 0
    } else if (cap !== shownCap) {
      const n = Number(cap)
      if (cap.trim() === '' || !Number.isInteger(n) || n < MIN_SESSIONS || n > MAX_SESSIONS) {
        setMsg({ ok: false, text: `Max concurrent session pods must be a whole number from ${MIN_SESSIONS} to ${MAX_SESSIONS}.` })
        return
      }
      capChange = n
    }
    setBusy(true)
    try {
      const r = await apiFetch('/api/cluster/settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          pod_idle_timeout_seconds: Math.round(Number(idle) * 3600),
          pod_ttl_seconds: Math.round(Number(ttl) * 3600),
          ...(capChange === undefined ? {} : { max_sessions: capChange }),
        }),
      })
      const body = await r.json().catch(() => ({}))
      if (!r.ok) {
        setMsg({ ok: false, text: body.error || (r.status === 403 ? 'Only an administrator can change this.' : 'Could not save.') })
        return
      }
      onSaved(body as ClusterStatusApi)
      // The sidebar's Cluster pill reads the cap on its own; tell it, so it shows the new one at once.
      window.dispatchEvent(new CustomEvent('blerg:cluster-settings-saved', { detail: body }))
      setMsg({ ok: true, text: 'Saved. Applies to sessions started from now on.' })
    } catch {
      setMsg({ ok: false, text: 'Could not save.' })
    } finally {
      setBusy(false)
    }
  }

  const input: React.CSSProperties = {
    width: 80, background: 'var(--basalt)', color: 'var(--chalk)',
    border: '1px solid var(--stone)', borderRadius: 4, padding: '2px 6px', textAlign: 'right',
  }
  return (
    <div style={card}>
      <span style={sectionLabel}>Session pod limits (administrators)</span>
      <div style={row}>
        <label htmlFor="pod-idle-hours">End a pod after idle (hours, 0 = never)</label>
        <input id="pod-idle-hours" type="number" min={0} step={0.5} value={idle} onChange={e => setIdle(e.target.value)} style={input} />
      </div>
      <div style={row}>
        <label htmlFor="pod-ttl-hours">Hard lifetime cap (hours)</label>
        <input id="pod-ttl-hours" type="number" min={1} step={1} value={ttl} onChange={e => setTtl(e.target.value)} style={input} />
      </div>
      <div style={{ ...row, borderBottom: 'none', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        <label htmlFor="max-session-pods">Max concurrent session pods</label>
        <span style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', justifyContent: 'flex-end' }}>
          {status.active_sessions >= 0 && <span style={{ color: 'var(--fog)', fontSize: '0.75rem' }}>{status.active_sessions} in use</span>}
          {capDefault != null && <span style={{ color: 'var(--fog)', fontSize: '0.75rem' }}>default {capDefault}</span>}
          {capOverridden && !resetCap && (
            <button type="button" onClick={() => { setCap(String(capDefault)); setResetCap(true) }}>Use default</button>
          )}
          <input
            id="max-session-pods" type="number" min={MIN_SESSIONS} max={MAX_SESSIONS} step={1} value={cap}
            onChange={e => { setCap(e.target.value); setResetCap(false) }} style={input}
          />
        </span>
      </div>
      <p style={{ color: 'var(--fog)', fontSize: '0.75rem', margin: '8px 0' }}>
        Idle means no message sent and no turn finished. An ended session keeps its history and can be resumed.
        {status.cpu_request && status.mem_request && (
          <> Each session pod asks for about {fmtCPU(status.cpu_request)} and {fmtMem(status.mem_request)}, so more pods need more cluster capacity.</>
        )}
      </p>
      <button onClick={save} disabled={busy}>{busy ? 'Saving…' : 'Save'}</button>
      {msg && <span role="status" style={{ marginLeft: 10, fontSize: '0.8rem', color: msg.ok ? 'var(--fog)' : 'var(--danger)' }}>{msg.text}</span>}
    </div>
  )
}

export default function ClusterStatus() {
  const [status, setStatus] = useState<ClusterStatusApi | null>(null)
  const [sessions, setSessions] = useState<ClusterSession[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(() => {
    apiFetch('/api/cluster/status')
      .then(r => r.json())
      .then((s: ClusterStatusApi) => {
        setStatus(s)
        if (s.configured) {
          // Not daemon_id: each cluster session pod connects as its own
          // ephemeral per-session daemon, so a fixed daemon_id only matches
          // a session in the brief window before its pod connects.
          // runtime=cluster lists every cluster-runtime session regardless
          // of which per-pod daemon currently owns it.
          return apiFetch('/api/sessions?runtime=cluster')
            .then(r => r.json())
            .then((rows: ClusterSession[]) => setSessions(rows))
        }
        setSessions([])
      })
      .catch(err => setError('Failed to load cluster status: ' + (err instanceof Error ? err.message : String(err))))
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => {
    load()
    const id = setInterval(load, 15000)
    return () => clearInterval(id)
  }, [load])

  return (
    <div style={{ minHeight: '100vh', background: 'var(--basalt)', color: 'var(--chalk)', paddingBottom: 72 }}>
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          padding: '12px 16px',
          borderBottom: '1px solid var(--stone)',
          position: 'sticky',
          top: 0,
          background: 'var(--basalt)',
          zIndex: 10,
        }}
      >
        <span style={{ fontSize: '1.1rem', fontWeight: 700, color: 'var(--blaze)' }}>Cluster</span>
      </div>

      <div style={{ padding: 16, maxWidth: 720, margin: '0 auto' }}>
        {loading && <div style={{ color: 'var(--fog)', textAlign: 'center', padding: 32 }}>Loading...</div>}
        {error && <div style={{ color: 'var(--danger)', marginBottom: 16 }}>{error}</div>}

        {!loading && status && !status.configured && (
          <div style={card}>
            <span style={sectionLabel}>Not configured</span>
            <p style={{ margin: 0, color: 'var(--fog)', fontSize: '0.9rem', lineHeight: 1.5 }}>
              This server has no cluster runtime — either it isn't running inside a
              k8s cluster, or <code>BLERG_RUNNER_AGENT_IMAGE</code> isn't set. See{' '}
              <code>install/k8s/CLUSTER-RUNTIME.md</code> for setup.
            </p>
          </div>
        )}

        {!loading && status && status.configured && (
          <>
            <div style={card}>
              <span style={sectionLabel}>Configuration</span>
              <div style={row}><span>Namespace</span><span>{status.namespace}</span></div>
              <div style={row}><span>Image</span><span style={{ wordBreak: 'break-all', textAlign: 'right' }}>{status.image}</span></div>
              <div style={row}><span>Secret</span><span>{status.secret_name}{status.oauth_secret_name && status.oauth_secret_name !== status.secret_name ? ` + ${status.oauth_secret_name}` : ''}</span></div>
              <div style={row}><span>Max concurrent sessions</span><span>{status.max_sessions}</span></div>
              <div style={row}><span>CPU / memory request</span><span>{status.cpu_request} / {status.mem_request}</span></div>
              <div style={row}><span>CPU / memory limit</span><span>{status.cpu_limit} / {status.mem_limit}</span></div>
              <div style={row}><span>Pod idle timeout</span><span>{status.pod_idle_timeout_seconds ? fmtSeconds(status.pod_idle_timeout_seconds) : 'never'}</span></div>
              <div style={row}><span>Pod lifetime cap</span><span>{fmtSeconds(status.pod_ttl_seconds)}</span></div>
              <div style={row}><span>Termination grace</span><span>{fmtSeconds(status.termination_grace_seconds)}</span></div>
              <div style={{ ...row, borderBottom: 'none' }}><span>Finished Job cleanup after</span><span>{fmtSeconds(status.ttl_seconds_after_finished)}</span></div>
            </div>

            <PodLimits
              key={`${status.pod_idle_timeout_seconds}-${status.pod_ttl_seconds}-${status.max_sessions}`}
              status={status}
              onSaved={s => setStatus(prev => ({ ...prev, ...s, active_sessions: prev?.active_sessions ?? 0, available_engines: prev?.available_engines ?? [] }))}
            />

            <div style={card}>
              <span style={sectionLabel}>Available engines</span>
              <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                {ALL_ENGINES.map(id => {
                  const available = (status.available_engines ?? []).includes(id)
                  return (
                    <span
                      key={id}
                      style={{
                        padding: '4px 12px',
                        borderRadius: 6,
                        fontSize: '0.8rem',
                        fontWeight: 600,
                        border: `1px solid ${available ? 'var(--blaze)' : 'var(--stone)'}`,
                        color: available ? 'var(--amber)' : 'var(--fog-dim)',
                        background: available ? 'color-mix(in srgb, var(--blaze) 16%, transparent)' : 'transparent',
                      }}
                    >
                      {ENGINE_LABELS[id]} {available ? '✓' : '—'}
                    </span>
                  )
                })}
                {/* Git isn't an engine, but it lives here for the same
                    reason: it's a credential the operator Secret either
                    carries or doesn't, and a user can supply their own. */}
                <span
                  style={{
                    padding: '4px 12px',
                    borderRadius: 6,
                    fontSize: '0.8rem',
                    fontWeight: 600,
                    border: `1px solid ${status.git_configured ? 'var(--blaze)' : 'var(--stone)'}`,
                    color: status.git_configured ? 'var(--amber)' : 'var(--fog-dim)',
                    background: status.git_configured ? 'color-mix(in srgb, var(--blaze) 16%, transparent)' : 'transparent',
                  }}
                >
                  Git: {status.git_configured ? 'configured' : 'not configured'}
                </span>
              </div>
              <p style={{ margin: '10px 0 0', color: 'var(--fog)', fontSize: '0.78rem' }}>
                OpenClaw isn't supported on cluster runtime (no credential wiring, not
                installed in the pod image). A "—" above means that engine's credential
                key wasn't found in the configured Secret — see CLUSTER-RUNTIME.md.
              </p>
            </div>

            <div style={card}>
              <span style={sectionLabel}>Running sessions</span>
              <div style={{ marginBottom: 8, fontSize: '0.85rem', color: 'var(--fog)' }}>
                {status.active_sessions < 0
                  ? 'Active Job count unavailable (check RBAC — see CLUSTER-RUNTIME.md)'
                  : `${status.active_sessions} active Job${status.active_sessions === 1 ? '' : 's'} of ${status.max_sessions} max`}
              </div>
              {sessions.length === 0 ? (
                <div style={{ color: 'var(--fog)', fontSize: '0.85rem' }}>No cluster sessions recorded.</div>
              ) : (
                sessions.map(s => (
                  <div key={s.id} style={row}>
                    <span>
                      <strong>{s.title || repoLabel(s.repo)}</strong>{' '}
                      <span style={{ color: 'var(--fog)' }}>
                        {repoLabel(s.repo)}{s.engine ? ` · ${ENGINE_LABELS[s.engine] ?? s.engine}` : ''}
                      </span>
                    </span>
                    <span style={{ color: s.status === 'running' ? 'var(--amber)' : 'var(--fog)' }}>{s.status}</span>
                  </div>
                ))
              )}
            </div>
          </>
        )}
      </div>
    </div>
  )
}
