import { useEffect, useRef, useState } from 'react'
import type { CSSProperties, FormEvent, KeyboardEvent } from 'react'
import { apiFetch } from '../apiFetch'

// ReposRootEditor shows where a daemon keeps repos for new sessions and lets
// the person change it: PUT /api/daemons/{id}/repos-root. The daemon itself
// validates the folder (it can create a missing one) and saves the choice so
// it survives a restart; its reason for refusing is shown inline as is.
// Sessions already running are unaffected.

export interface ReposRootEditorProps {
  daemonId: string
  daemonName: string
  reposRoot: string
  onChanged: (reposRoot: string) => void
}

const rowStyle: CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  gap: 8,
  marginTop: 8,
  fontSize: '0.75rem',
  color: 'var(--fog)',
}

const pathStyle: CSSProperties = {
  fontFamily: 'var(--mono)',
  color: 'var(--chalk)',
  overflowWrap: 'anywhere',
  flex: 1,
  minWidth: 0,
}

const linkButton: CSSProperties = {
  background: 'none',
  border: 'none',
  padding: 0,
  color: 'var(--amber)',
  cursor: 'pointer',
  fontFamily: 'inherit',
  fontSize: '0.75rem',
  textDecoration: 'underline',
  flexShrink: 0,
}

const smallButton = (primary: boolean, disabled = false): CSSProperties => ({
  background: primary && !disabled ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'var(--scree)',
  border: `1px solid ${primary && !disabled ? 'var(--blaze)' : 'var(--stone)'}`,
  borderRadius: 6,
  color: primary && !disabled ? 'var(--amber)' : 'var(--fog)',
  padding: '4px 10px',
  cursor: disabled ? 'not-allowed' : 'pointer',
  fontFamily: 'inherit',
  fontSize: '0.75rem',
  flexShrink: 0,
})

const APPLIED_MS = 4000

export default function ReposRootEditor({ daemonId, daemonName, reposRoot, onChanged }: ReposRootEditorProps) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(reposRoot)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [applied, setApplied] = useState(false)
  const appliedTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Another daemon picked: drop whatever was being edited for the last one.
  const [shownDaemonId, setShownDaemonId] = useState(daemonId)
  if (shownDaemonId !== daemonId) {
    setShownDaemonId(daemonId)
    setEditing(false)
    setError(null)
    setApplied(false)
  }

  useEffect(() => () => {
    if (appliedTimer.current) clearTimeout(appliedTimer.current)
  }, [])

  const startEdit = () => {
    setDraft(reposRoot)
    setError(null)
    setApplied(false)
    setEditing(true)
  }

  const cancel = () => {
    setEditing(false)
    setError(null)
  }

  const trimmed = draft.trim()
  const unchanged = trimmed === reposRoot
  const canSave = !saving && trimmed !== '' && !unchanged

  const save = async (e?: FormEvent) => {
    e?.preventDefault()
    if (!canSave) return
    setSaving(true)
    setError(null)
    try {
      const resp = await apiFetch(`/api/daemons/${encodeURIComponent(daemonId)}/repos-root`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ repos_root: trimmed }),
      })
      const body = await resp.json().catch(() => ({}))
      if (!resp.ok) {
        setError(typeof body?.error === 'string' && body.error ? body.error : `Couldn't change it (HTTP ${resp.status})`)
        return
      }
      const now = typeof body?.repos_root === 'string' && body.repos_root ? body.repos_root : trimmed
      setEditing(false)
      setApplied(true)
      if (appliedTimer.current) clearTimeout(appliedTimer.current)
      appliedTimer.current = setTimeout(() => setApplied(false), APPLIED_MS)
      onChanged(now)
    } catch (err) {
      setError(`Couldn't reach the server: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setSaving(false)
    }
  }

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Escape') {
      e.preventDefault()
      cancel()
    }
  }

  if (!editing) {
    return (
      <div data-testid="repos-root">
        <div style={rowStyle}>
          <span style={{ flexShrink: 0 }}>Repos folder</span>
          <span data-testid="repos-root-value" style={pathStyle}>{reposRoot || '—'}</span>
          <button
            type="button"
            data-testid="repos-root-edit"
            aria-label={`Change ${daemonName}'s repos folder`}
            onClick={startEdit}
            style={linkButton}
          >
            Change
          </button>
        </div>
        {applied && (
          <p data-testid="repos-root-applied" role="status" style={{ margin: '4px 0 0', fontSize: '0.72rem', color: 'var(--amber)' }}>
            ✓ Applied — new sessions on {daemonName} use this folder.
          </p>
        )}
      </div>
    )
  }

  return (
    <form data-testid="repos-root" onSubmit={save} style={{ marginTop: 8 }}>
      <label htmlFor={`repos-root-input-${daemonId}`} style={{ display: 'block', fontSize: '0.75rem', color: 'var(--fog)', marginBottom: 4 }}>
        Repos folder on {daemonName}
      </label>
      <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
        <input
          id={`repos-root-input-${daemonId}`}
          data-testid="repos-root-input"
          type="text"
          value={draft}
          autoFocus
          spellCheck={false}
          autoComplete="off"
          placeholder="/home/you/repositories"
          disabled={saving}
          onChange={e => setDraft(e.target.value)}
          onKeyDown={onKeyDown}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? `repos-root-error-${daemonId}` : undefined}
          style={{
            flex: 1,
            minWidth: 0,
            background: 'var(--basalt)',
            border: `1px solid ${error ? 'var(--danger)' : 'var(--stone)'}`,
            borderRadius: 6,
            color: 'var(--chalk)',
            padding: '5px 8px',
            fontFamily: 'var(--mono)',
            fontSize: '0.8rem',
          }}
        />
        <button type="submit" data-testid="repos-root-save" disabled={!canSave} style={smallButton(true, !canSave)}>
          {saving ? 'Saving…' : 'Save'}
        </button>
        <button type="button" data-testid="repos-root-cancel" onClick={cancel} disabled={saving} style={smallButton(false, saving)}>
          Cancel
        </button>
      </div>
      {error ? (
        <p id={`repos-root-error-${daemonId}`} data-testid="repos-root-error" role="alert" style={{ margin: '4px 0 0', fontSize: '0.72rem', color: 'var(--danger)' }}>
          {error}
        </p>
      ) : (
        <p style={{ margin: '4px 0 0', fontSize: '0.72rem', color: 'var(--fog-dim)' }}>
          An absolute path on that machine; it's created if missing. Running sessions aren't moved.
        </p>
      )}
    </form>
  )
}
