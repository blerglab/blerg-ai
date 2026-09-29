import { useState, useEffect } from 'react'
import { createBoard } from '../lib/boardApi'
import { apiFetch } from '../apiFetch'
import { useBackdropClose } from '../hooks/useBackdropClose'

interface RepoInfo {
  name: string
  full_name: string
  checked_out: string[]
  is_local?: boolean
}

interface DaemonApiInfo {
  id: string
  name: string
  mode: string
  repos_root: string
  status: string
}

interface BoardCreateSheetProps {
  open: boolean
  onClose: () => void
  onCreated: () => void
}

const sectionLabel: React.CSSProperties = {
  color: 'var(--fog)',
  fontSize: '0.7rem',
  fontWeight: 700,
  letterSpacing: '0.1em',
  textTransform: 'uppercase',
  marginBottom: 8,
  display: 'block',
}

export default function BoardCreateSheet({ open, onClose, onCreated }: BoardCreateSheetProps) {
  const backdrop = useBackdropClose(onClose)
  const [repos, setRepos] = useState<RepoInfo[]>([])
  const [daemons, setDaemons] = useState<DaemonApiInfo[]>([])
  const [loading, setLoading] = useState(true)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [selectedRepos, setSelectedRepos] = useState<Set<string>>(new Set())
  const [selectedDaemonId, setSelectedDaemonId] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return

    Promise.all([
      apiFetch('/api/repos').then(r => r.json()),
      apiFetch('/api/daemons').then(r => r.json()),
    ])
      .then(([repoData, daemonData]) => {
        setRepos(repoData.repos ?? [])
        setDaemons(daemonData)
      })
      .catch(err => {
        setError('Failed to load data: ' + err.message)
      })
      .finally(() => setLoading(false))
  }, [open])

  function toggleRepo(fullName: string) {
    setSelectedRepos(prev => {
      const next = new Set(prev)
      if (next.has(fullName)) {
        next.delete(fullName)
      } else {
        next.add(fullName)
      }
      return next
    })
  }

  const canSubmit = name.trim() !== '' && selectedRepos.size >= 1 && !submitting

  async function handleSubmit() {
    if (!canSubmit) return
    setSubmitting(true)
    setError(null)
    try {
      await createBoard({
        name: name.trim(),
        repos: [...selectedRepos],
        description: description.trim() || null,
        defaultDaemonId: selectedDaemonId || null,
      })
      onCreated()
      onClose()
    } catch (err: unknown) {
      setError('Failed to create board: ' + (err instanceof Error ? err.message : String(err)))
    } finally {
      setSubmitting(false)
    }
  }

  const itemBase: React.CSSProperties = {
    background: 'var(--scree)',
    border: '1px solid var(--stone)',
    borderRadius: 8,
    padding: '10px 12px',
    marginBottom: 6,
    cursor: 'pointer',
    color: 'var(--chalk)',
  }

  const itemSelected: React.CSSProperties = {
    ...itemBase,
    border: '1px solid var(--blaze)',
    background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
  }

  return (
    <div
      style={{
        position: 'fixed',
        inset: 0,
        zIndex: 100,
        background: 'rgba(5,4,3,0.75)',
        display: open ? 'flex' : 'none',
        alignItems: 'flex-end',
      }}
      {...backdrop}
    >
      <div
        style={{
          background: 'var(--basalt)',
          width: '100%',
          maxHeight: '90vh',
          borderRadius: '16px 16px 0 0',
          overflowY: 'auto',
          padding: 16,
          border: '1px solid var(--stone)',
          borderBottom: 'none',
        }}
      >
        {/* Header */}
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
          <span style={{ color: 'var(--blaze)', fontWeight: 700, fontSize: '1rem', letterSpacing: '0.08em', textTransform: 'uppercase' }}>New Board</span>
          <button
            onClick={onClose}
            style={{ background: 'none', border: 'none', color: 'var(--fog)', fontSize: '1.4rem', cursor: 'pointer', fontFamily: 'inherit' }}
          >
            ✕
          </button>
        </div>

        {loading ? (
          <div style={{ color: 'var(--fog)', padding: 32, textAlign: 'center' }}>Loading...</div>
        ) : (
          <>
            {/* Board Name */}
            <div style={{ marginBottom: 20 }}>
              <span style={sectionLabel}>Board Name</span>
              <input
                value={name}
                onChange={e => setName(e.target.value)}
                placeholder="Board name"
                autoCorrect="off"
                autoCapitalize="none"
                autoComplete="off"
                style={{
                  width: '100%',
                  background: 'var(--scree)',
                  border: '1px solid var(--stone)',
                  borderRadius: 8,
                  color: 'var(--chalk)',
                  padding: '8px 12px',
                  boxSizing: 'border-box',
                  fontFamily: 'inherit',
                }}
              />
            </div>

            {/* Description */}
            <div style={{ marginBottom: 20 }}>
              <span style={sectionLabel}>Description (optional)</span>
              <textarea
                value={description}
                onChange={e => setDescription(e.target.value)}
                placeholder="What is this board for?"
                autoCorrect="off"
                autoComplete="off"
                spellCheck={false}
                style={{
                  width: '100%',
                  background: 'var(--scree)',
                  border: '1px solid var(--stone)',
                  borderRadius: 8,
                  color: 'var(--chalk)',
                  padding: '8px 12px',
                  resize: 'none',
                  height: 64,
                  fontFamily: 'system-ui',
                  boxSizing: 'border-box',
                }}
              />
            </div>

            {/* Repos multi-select */}
            <div style={{ marginBottom: 20 }}>
              <span style={sectionLabel}>Repositories (select one or more)</span>
              <div style={{ maxHeight: 200, overflowY: 'auto' }}>
                {repos.map(repo => {
                  const isSelected = selectedRepos.has(repo.full_name)
                  return (
                    <div
                      key={repo.full_name}
                      style={isSelected ? itemSelected : itemBase}
                      onClick={() => toggleRepo(repo.full_name)}
                    >
                      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                        <span style={{
                          width: 14,
                          height: 14,
                          borderRadius: 3,
                          border: `1px solid ${isSelected ? 'var(--blaze)' : 'var(--fog-dim)'}`,
                          background: isSelected ? 'var(--blaze)' : 'transparent',
                          display: 'inline-flex',
                          alignItems: 'center',
                          justifyContent: 'center',
                          flexShrink: 0,
                          fontSize: '0.65rem',
                          color: 'var(--basalt)',
                        }}>
                          {isSelected ? '✓' : ''}
                        </span>
                        <span style={{ fontWeight: 600, fontSize: '0.9rem' }}>{repo.name}</span>
                        {repo.is_local && (
                          <span style={{
                            fontSize: '0.65rem',
                            color: 'var(--fog)',
                            border: '1px solid var(--stone)',
                            borderRadius: 4,
                            padding: '1px 5px',
                            letterSpacing: '0.05em',
                          }}>local</span>
                        )}
                      </div>
                      <div style={{ fontSize: '0.72rem', marginTop: 2, color: 'var(--fog-dim)', paddingLeft: 22 }}>
                        {repo.full_name}
                      </div>
                    </div>
                  )
                })}
                {repos.length === 0 && (
                  <div style={{ color: 'var(--fog)', fontSize: '0.85rem', padding: '8px 0' }}>No repos available.</div>
                )}
              </div>
            </div>

            {/* Default daemon (optional) */}
            {daemons.length > 0 && (
              <div style={{ marginBottom: 20 }}>
                <span style={sectionLabel}>Default Daemon (optional)</span>
                {daemons.map(daemon => {
                  const isSelected = selectedDaemonId === daemon.id
                  return (
                    <div
                      key={daemon.id}
                      style={isSelected ? itemSelected : itemBase}
                      onClick={() => setSelectedDaemonId(isSelected ? null : daemon.id)}
                    >
                      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                        <span style={{
                          width: 8,
                          height: 8,
                          borderRadius: '50%',
                          background: 'var(--lichen)',
                          display: 'inline-block',
                          flexShrink: 0,
                        }} />
                        <span style={{ fontWeight: 600, fontSize: '0.9rem' }}>{daemon.name}</span>
                        <span style={{ fontSize: '0.72rem', color: 'var(--fog-dim)' }}>{daemon.mode}</span>
                      </div>
                    </div>
                  )
                })}
              </div>
            )}

            {/* Submit */}
            <button
              onClick={handleSubmit}
              disabled={!canSubmit}
              style={{
                width: '100%',
                background: canSubmit ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'var(--basalt)',
                color: canSubmit ? 'var(--amber)' : 'var(--fog-dim)',
                border: `1px solid ${canSubmit ? 'var(--blaze)' : 'var(--stone)'}`,
                borderRadius: 8,
                padding: '12px',
                fontWeight: 700,
                fontSize: '0.95rem',
                cursor: canSubmit ? 'pointer' : 'not-allowed',
                fontFamily: 'inherit',
                letterSpacing: '0.05em',
              }}
            >
              {submitting ? 'Creating...' : 'Create Board'}
            </button>

            {error && (
              <div style={{ color: 'var(--danger)', marginTop: 8, fontSize: 13 }}>{error}</div>
            )}
          </>
        )}
      </div>
    </div>
  )
}
