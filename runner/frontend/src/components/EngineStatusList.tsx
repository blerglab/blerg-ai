// EngineStatusList: engines a target can't run, each with the server's reason, the exact fix
// and its one-click action — "Check again" (the desktop re-detects now, not at its next
// heartbeat) and "Re-detect PATH" (it reads your login shell's PATH first). The launch sheet
// shows the engines a launch can't use; Settings shows every engine per desktop.
import { useState, type ReactNode } from 'react'
import { checkEngines, ENGINE_STATUS_LABELS, keepNote, keptNote, type EngineStatusRow } from '../lib/engineStatus'
import './EngineStatusList.css'

/** `code` spans in a server-written fix, as <code>. Everything else stays text. */
export function FixText({ text }: { text: string }) {
  const parts = text.split('`')
  return <>{parts.map((part, i) => i % 2 === 1 ? <code key={i}>{part}</code> : <span key={i}>{part}</span>)}</>
}

interface Props {
  rows: EngineStatusRow[]
  /** Called after a check so the caller re-reads what it shows (launch options, the desktop list). */
  onChecked: () => void
  /** Rendered for an engine whose fix is in Settings (the Cluster's connections). */
  settingsLink?: ReactNode
  /** The desktop to ask when a row names none (Settings rows are per desktop). */
  daemonId?: string
  /** Show the disabled engine pill on each row (the launch sheet) or the status word (Settings). */
  pills?: boolean
  /** Per-row Check again / Re-detect PATH (off where the caller offers them once, for a whole desktop). */
  actions?: boolean
  testId?: string
}

export default function EngineStatusList({ rows, onChecked, settingsLink, daemonId, pills = false, actions = true, testId = 'engine-status-list' }: Props) {
  const [busy, setBusy] = useState('')
  const [, setVersion] = useState(0)
  const setNote = (row: EngineStatusRow, note: { text: string; error?: boolean }) => {
    keepNote(row, daemonId, note)
    setVersion(v => v + 1)
  }
  if (rows.length === 0) return null

  async function check(row: EngineStatusRow, redetectPath: boolean) {
    const target = row.daemon_id ?? daemonId
    const key = `${row.id}:${redetectPath ? 'path' : 'check'}`
    if (busy) return
    setBusy(key)
    setNote(row, { text: redetectPath ? 'Reading your login shell’s PATH…' : 'Checking…' })
    try {
      if (target) {
        const outcome = await checkEngines(target, redetectPath)
        const now = outcome.rows.find(r => r.id === row.id)
        let note = now?.status === 'available' ? `${row.name} is ready.` : now ? `Still unavailable: ${now.reason}` : 'Checked.'
        if (redetectPath && outcome.pathError) note = outcome.pathError
        else if (redetectPath) note = (outcome.pathAdded.length ? `Added ${outcome.pathAdded.join(', ')} to the daemon’s PATH. ` : 'No new folders on your login shell’s PATH. ') + note
        setNote(row, { text: note, error: !!(redetectPath && outcome.pathError) })
      } else {
        setNote(row, { text: 'Checked.' })
      }
      onChecked()
    } catch (err) {
      setNote(row, { text: err instanceof Error ? err.message : 'Check failed. Try again.', error: true })
    } finally {
      setBusy('')
    }
  }

  return (
    <ul className="engine-status-list" data-testid={testId}>
      {rows.map(row => {
        const note = keptNote(row, daemonId)
        const canCheck = actions && row.status !== 'unsupported' && row.status !== 'available' && (!!(row.daemon_id ?? daemonId) || row.action === 'settings')
        return (
          <li key={row.id} className={`engine-status-row is-${row.status}`} data-testid={`engine-status-${row.id}`}>
            <div className="engine-status-head">
              {pills
                ? <button type="button" className="settings-action engine-status-pill" disabled aria-disabled="true"
                    data-testid={`engine-unavailable-${row.id}`} title={row.reason}>{row.name}</button>
                : <span className="engine-status-name">{row.name}</span>}
              <span className="engine-status-word">{ENGINE_STATUS_LABELS[row.status] ?? row.status}</span>
            </div>
            {row.reason && <p className="engine-status-reason">{row.reason}</p>}
            {row.fix && <p className="engine-status-fix"><FixText text={row.fix} />{row.action === 'settings' && settingsLink ? <> ({settingsLink})</> : null}</p>}
            {canCheck && (
              <div className="engine-status-actions">
                <button type="button" className="settings-action btn small" disabled={!!busy} data-testid={`engine-check-${row.id}`}
                  onClick={() => void check(row, false)}>{busy === `${row.id}:check` ? 'Checking…' : 'Check again'}</button>
                {row.action === 'redetect_path' && (row.daemon_id ?? daemonId) && (
                  <button type="button" className="settings-action btn small" disabled={!!busy} data-testid={`engine-redetect-${row.id}`}
                    onClick={() => void check(row, true)}>{busy === `${row.id}:path` ? 'Re-detecting…' : 'Re-detect PATH'}</button>
                )}
              </div>
            )}
            {note && <p role="status" className={'engine-status-note' + (note.error ? ' is-error' : '')}>{note.text}</p>}
            {row.searched_path && row.searched_path.length > 0 && (
              <details className="engine-status-path">
                <summary>PATH the daemon searched ({row.searched_path.length})</summary>
                <code>{row.searched_path.join(':')}</code>
              </details>
            )}
          </li>
        )
      })}
    </ul>
  )
}
