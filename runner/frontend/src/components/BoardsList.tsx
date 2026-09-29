import { useState, useEffect, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import type { Board } from '../types'
import { listBoards, deleteBoard } from '../lib/boardApi'
import BoardCreateSheet from './BoardCreateSheet'

export default function BoardsList() {
  const navigate = useNavigate()
  const [boards, setBoards] = useState<Board[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [createOpen, setCreateOpen] = useState(false)

  const fetchBoards = useCallback(() => {
    listBoards()
      .then(setBoards)
      .catch(err => setError('Failed to load boards: ' + (err instanceof Error ? err.message : String(err))))
      .finally(() => setLoading(false))
  }, [])

  // Reload after a change: back to the loading state, last error dropped.
  const loadBoards = () => {
    setLoading(true)
    setError(null)
    fetchBoards()
  }

  // First load: the initial state is already "loading, no error".
  useEffect(() => {
    fetchBoards()
  }, [fetchBoards])

  async function handleDelete(board: Board) {
    const confirmed = window.confirm(`Delete board "${board.name}"? This cannot be undone.`)
    if (!confirmed) return
    try {
      await deleteBoard(board.id)
      loadBoards()
    } catch (err: unknown) {
      setError('Failed to delete board: ' + (err instanceof Error ? err.message : String(err)))
    }
  }

  return (
    <div
      style={{
        minHeight: '100vh',
        background: 'var(--basalt)',
        color: 'var(--chalk)',
      }}
    >
      {/* Header */}
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
        <span
          style={{
            color: 'var(--blaze)',
            fontWeight: 700,
            fontSize: '1rem',
            letterSpacing: '0.1em',
            textTransform: 'uppercase',
          }}
        >
          Boards
        </span>
        <button
          onClick={() => setCreateOpen(true)}
          style={{
            background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
            color: 'var(--amber)',
            border: '1px solid var(--blaze)',
            borderRadius: 6,
            padding: '6px 14px',
            fontWeight: 600,
            cursor: 'pointer',
            fontSize: '0.9rem',
            letterSpacing: '0.05em',
            fontFamily: 'inherit',
          }}
        >
          +
        </button>
      </div>

      {/* Content */}
      <div style={{ padding: '12px 16px' }}>
        {loading && (
          <div style={{ color: 'var(--fog)', padding: 32, textAlign: 'center' }}>Loading boards...</div>
        )}

        {error && (
          <div style={{ color: 'var(--danger)', padding: '8px 0', fontSize: 13 }}>{error}</div>
        )}

        {!loading && boards.length === 0 && !error && (
          <div style={{ color: 'var(--fog)', padding: '32px 0', textAlign: 'center' }}>
            No boards yet. Create one with the + button.
          </div>
        )}

        {boards.map(board => (
          <div
            key={board.id}
            style={{
              background: 'var(--scree)',
              border: '1px solid var(--stone)',
              borderRadius: 8,
              padding: '12px 14px',
              marginBottom: 10,
              cursor: 'pointer',
              position: 'relative',
            }}
            onClick={() => navigate('/boards/' + board.id)}
          >
            {/* Board name */}
            <div
              style={{
                fontWeight: 700,
                fontSize: '0.95rem',
                color: 'var(--chalk)',
                marginBottom: 6,
                paddingRight: 32,
              }}
            >
              {board.name}
            </div>

            {/* Description */}
            {board.description && (
              <div
                style={{
                  fontSize: '0.8rem',
                  color: 'var(--fog)',
                  marginBottom: 6,
                }}
              >
                {board.description}
              </div>
            )}

            {/* Repo chips */}
            {board.repos.length > 0 && (
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, marginBottom: 4 }}>
                {board.repos.map(repo => (
                  <span
                    key={repo}
                    style={{
                      fontSize: '0.68rem',
                      color: 'var(--fog)',
                      border: '1px solid var(--stone)',
                      borderRadius: 4,
                      padding: '1px 6px',
                      letterSpacing: '0.03em',
                      background: 'var(--basalt)',
                    }}
                  >
                    {repo}
                  </span>
                ))}
              </div>
            )}

            {/* Delete button */}
            <button
              aria-label="Delete board"
              onClick={e => {
                e.stopPropagation()
                handleDelete(board)
              }}
              style={{
                position: 'absolute',
                top: 10,
                right: 10,
                background: 'none',
                border: 'none',
                color: 'var(--fog-dim)',
                fontSize: '1rem',
                cursor: 'pointer',
                padding: '2px 6px',
                borderRadius: 4,
                fontFamily: 'inherit',
                lineHeight: 1,
              }}
            >
              ✕
            </button>
          </div>
        ))}
      </div>

      {/* Create sheet — remounted on open/close so form state resets */}
      <BoardCreateSheet
        key={createOpen ? 'open' : 'closed'}
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={() => {
          setCreateOpen(false)
          loadBoards()
        }}
      />
    </div>
  )
}
