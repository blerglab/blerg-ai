// ArchiveView — paginated list of archived tickets for a board.
// Route: /boards/:id/archive

import { useEffect, useState } from 'react'
import { useParams, useNavigate, Link } from 'react-router-dom'
import * as boardApi from '../lib/boardApi'
import type { Ticket } from '../types'

// ── ArchiveView ───────────────────────────────────────────────────────────────

export default function ArchiveView() {
  const { id: boardId = '' } = useParams<{ id: string }>()
  const navigate = useNavigate()

  const [tickets, setTickets] = useState<Ticket[]>([])
  const [cursor, setCursor] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [initialLoaded, setInitialLoaded] = useState(false)

  // A board to load (first render, or the route moved to another board):
  // show the loading state and drop the last error before the fetch below.
  const [loadingFor, setLoadingFor] = useState('')
  if (boardId && loadingFor !== boardId) {
    setLoadingFor(boardId)
    setLoading(true)
    setError(null)
  }

  // ── initial load ────────────────────────────────────────────────────────
  useEffect(() => {
    if (!boardId) return
    boardApi
      .getBoardArchive(boardId, {})
      .then((r) => {
        setTickets(r.tickets)
        setCursor(r.next_cursor)
        setInitialLoaded(true)
      })
      .catch((err: unknown) => {
        setError(err instanceof Error ? err.message : String(err))
      })
      .finally(() => setLoading(false))
  }, [boardId])

  // ── load more ───────────────────────────────────────────────────────────
  async function handleLoadMore() {
    if (!cursor || loading) return
    setLoading(true)
    try {
      const r = await boardApi.getBoardArchive(boardId, { cursor })
      setTickets((prev) => [...prev, ...r.tickets])
      setCursor(r.next_cursor)
    } catch {
      // ignore
    } finally {
      setLoading(false)
    }
  }

  // ── render ───────────────────────────────────────────────────────────────

  return (
    <div
      style={{
        minHeight: '100vh',
        background: 'var(--basalt)',
        color: 'var(--chalk)',
        fontFamily: 'inherit',
      }}
    >
      {/* Header */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 10,
          padding: '10px 16px',
          borderBottom: '1px solid var(--stone)',
          background: 'var(--basalt)',
        }}
      >
        <button
          data-testid="back-button"
          onClick={() => navigate(`/boards/${boardId}`)}
          style={{
            background: 'none',
            border: 'none',
            color: 'var(--fog-dim)',
            cursor: 'pointer',
            fontSize: '1rem',
            padding: '2px 6px',
            fontFamily: 'inherit',
          }}
        >
          ←
        </button>
        <span
          style={{
            color: 'var(--blaze)',
            fontWeight: 700,
            fontSize: '1rem',
            letterSpacing: '0.08em',
            textTransform: 'uppercase',
          }}
        >
          Archive
        </span>
      </div>

      {/* Content */}
      <div style={{ maxWidth: 720, margin: '0 auto', padding: '20px 16px' }}>
        {error && (
          <div style={{ color: 'var(--danger)', marginBottom: 16 }}>{error}</div>
        )}

        {initialLoaded && tickets.length === 0 && (
          <div
            data-testid="empty-archive"
            style={{ color: 'var(--stone)', fontSize: '0.9rem', textAlign: 'center', paddingTop: 40 }}
          >
            No archived tickets.
          </div>
        )}

        <div>
          {tickets.map((t) => (
            <Link
              key={t.id}
              data-testid={`ticket-row-${t.id}`}
              to={`/boards/${boardId}/ticket/${t.id}`}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 10,
                padding: '10px 14px',
                marginBottom: 6,
                background: 'var(--basalt)',
                border: '1px solid var(--basalt)',
                borderRadius: 6,
                textDecoration: 'none',
                color: 'var(--chalk)',
              }}
            >
              <span
                style={{
                  flex: 1,
                  fontSize: '0.9rem',
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  whiteSpace: 'nowrap',
                }}
              >
                {t.title}
              </span>
              <span style={{ fontSize: '0.72rem', color: 'var(--fog-dim)', flexShrink: 0 }}>
                {t.priority}
              </span>
              {t.archived_at && (
                <span style={{ fontSize: '0.7rem', color: 'var(--stone)', flexShrink: 0 }}>
                  {new Date(t.archived_at).toLocaleDateString()}
                </span>
              )}
            </Link>
          ))}
        </div>

        {cursor && (
          <button
            data-testid="load-more-archive"
            onClick={handleLoadMore}
            disabled={loading}
            style={{
              background: 'none',
              border: '1px solid var(--stone)',
              borderRadius: 6,
              color: 'var(--fog)',
              padding: '8px 16px',
              cursor: loading ? 'default' : 'pointer',
              fontSize: '0.85rem',
              fontFamily: 'inherit',
              marginTop: 10,
              width: '100%',
            }}
          >
            {loading ? 'Loading…' : 'Load more'}
          </button>
        )}
      </div>
    </div>
  )
}
