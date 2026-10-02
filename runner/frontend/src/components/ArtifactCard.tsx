// The transcript card for a published file: 📎 name · v3 · size · [Download] [View]. The version
// shows from v2 on, and on a v1 whose file has later versions; a lone v1 has none.
import { useContext, useState } from 'react'
import { ArtifactActionsContext } from '../lib/artifactActions'
import { downloadArtifact, formatSize } from '../lib/artifacts'
import { isViewable } from '../lib/artifactViews'
import type { ArtifactPayload } from '../types'
import { TimeLabel } from './TimeLabel'
import './Artifacts.css'

export default function ArtifactCard({ payload, ts }: { payload: ArtifactPayload; ts?: string }) {
  const actions = useContext(ArtifactActionsContext)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const version = payload.version
  const latest = actions?.latestVersion?.(payload) ?? payload.latest_version ?? version
  const showVersion = version !== undefined && (version > 1 || (latest ?? 1) > 1)

  async function download() {
    if (!actions) return
    setBusy(true)
    setError(null)
    try {
      await downloadArtifact(actions.sessionId, payload)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Download failed.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="agent-card artifact-card" data-testid="artifact-card">
      <div className="artifact-card-row">
        <span className="artifact-card-icon" aria-hidden="true">📎</span>
        <span className="artifact-card-name">{payload.name}</span>
        {showVersion && <>{' '}<span className="artifact-card-version" data-testid="artifact-card-version">· v{version}</span></>}
        {' '}<span className="artifact-card-size">· {formatSize(payload.size)}</span>
        {actions && (
          <span className="artifact-card-actions">
            <button type="button" disabled={busy} onClick={() => void download()}>Download</button>
            {isViewable(payload.view) && (
              <button type="button" onClick={() => actions.view(payload)}>View</button>
            )}
          </span>
        )}
        <TimeLabel ts={ts} className="card-time-end" />
      </div>
      {error && <div className="artifact-error" role="alert">{error}</div>}
    </div>
  )
}
