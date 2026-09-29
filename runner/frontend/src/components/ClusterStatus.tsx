import { useState, useEffect, useCallback } from 'react'
import { apiFetch } from '../apiFetch'
import { repoLabel } from '../lib/noRepo'

interface ClusterStatusApi {
  configured: boolean
  namespace?: string
  image?: string
  daemon_id?: string
  max_sessions?: number
  active_sessions: number
  available_engines: string[]
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
              <div style={row}><span>Pod TTL</span><span>{fmtSeconds(status.pod_ttl_seconds)}</span></div>
              <div style={row}><span>Termination grace</span><span>{fmtSeconds(status.termination_grace_seconds)}</span></div>
              <div style={{ ...row, borderBottom: 'none' }}><span>Finished Job cleanup after</span><span>{fmtSeconds(status.ttl_seconds_after_finished)}</span></div>
            </div>

            <div style={card}>
              <span style={sectionLabel}>Available engines</span>
              <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                {ALL_ENGINES.map(id => {
                  const available = status.available_engines.includes(id)
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
