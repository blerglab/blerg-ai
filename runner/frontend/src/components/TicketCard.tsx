import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import type { Ticket, Column, SessionInfo } from '../types'
import { moveTicket, newOpId } from '../lib/boardApi'
import { useBoardStore } from '../hooks/useBoardStore'

interface TicketCardProps {
  ticket: Ticket
  columns: Column[]
  sessions?: SessionInfo[]
}

const PRIORITY_COLORS: Record<string, string> = {
  low: 'var(--lichen)',
  medium: 'var(--amber)',
  high: 'var(--blaze)',
  urgent: 'var(--danger)',
}

const PRIORITY_LABELS: Record<string, string> = {
  low: 'L',
  medium: 'M',
  high: 'H',
  urgent: '!',
}

export default function TicketCard({ ticket, columns, sessions = [] }: TicketCardProps) {
  const navigate = useNavigate()
  const [moveOpen, setMoveOpen] = useState(false)
  const [moving, setMoving] = useState(false)
  const addPendingOp = useBoardStore((s) => s.addPendingOp)

  const boundSession = sessions.find((s) => s.id === ticket.session_id)
  const isSessionWaiting = boundSession?.status === 'waiting'

  const priorityColor = PRIORITY_COLORS[ticket.priority] ?? 'var(--fog-dim)'
  const otherColumns = columns.filter((c) => c.id !== ticket.column_id)

  async function handleMove(columnId: string) {
    setMoveOpen(false)
    setMoving(true)
    const opId = newOpId()
    addPendingOp(opId)
    try {
      await moveTicket(ticket.id, { columnId, opId })
    } catch (err) {
      console.error('Failed to move ticket:', err)
    } finally {
      setMoving(false)
    }
  }

  return (
    <div
      data-testid="ticket-card"
      onClick={() => navigate(`/boards/${ticket.board_id}/ticket/${ticket.id}`)}
      style={{
        background: 'var(--scree)',
        border: '1px solid var(--stone)',
        borderRadius: 8,
        padding: '10px 12px',
        marginBottom: 8,
        cursor: 'pointer',
        position: 'relative',
        borderLeft: `3px solid ${priorityColor}`,
        opacity: moving ? 0.6 : 1,
        userSelect: 'none',
      }}
    >
      {/* Session waiting indicator — amber "?" when the bound session needs attention */}
      {isSessionWaiting && (
        <span
          data-testid="session-indicator"
          title="Session waiting"
          style={{
            position: 'absolute',
            top: 8,
            right: 8,
            color: 'var(--blaze)',
            fontWeight: 800,
            fontSize: '0.9rem',
            lineHeight: 1,
          }}
        >
          ?
        </span>
      )}

      {/* First line: priority indicator + title + badges */}
      <div style={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 4 }}>
        {/* Priority indicator */}
        <span
          data-testid="priority-indicator"
          data-priority={ticket.priority}
          style={{
            display: 'inline-block',
            fontSize: '0.62rem',
            fontWeight: 700,
            color: priorityColor,
            border: `1px solid ${priorityColor}`,
            borderRadius: 3,
            padding: '0 4px',
            letterSpacing: '0.05em',
            lineHeight: '1.5',
            flexShrink: 0,
          }}
        >
          {PRIORITY_LABELS[ticket.priority] ?? ticket.priority.toUpperCase()}
        </span>

        {/* Title */}
        <span
          style={{
            fontSize: '0.88rem',
            fontWeight: 600,
            color: 'var(--chalk)',
            flex: 1,
            minWidth: 0,
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
          }}
        >
          {ticket.title}
        </span>

        {/* Blocked badge */}
        {ticket.blocked && (
          <span
            data-testid="blocked-badge"
            style={{
              display: 'inline-block',
              background: 'color-mix(in srgb, var(--danger) 22%, var(--basalt))',
              color: 'var(--danger)',
              border: '1px solid color-mix(in srgb, var(--danger) 35%, var(--scree))',
              borderRadius: 4,
              fontSize: '0.62rem',
              fontWeight: 700,
              padding: '0 5px',
              letterSpacing: '0.05em',
              textTransform: 'uppercase',
              lineHeight: '1.5',
              flexShrink: 0,
            }}
          >
            blocked
          </span>
        )}

        {/* Size badge */}
        {ticket.size && (
          <span
            data-testid="size-badge"
            style={{
              display: 'inline-block',
              background: 'var(--basalt)',
              color: 'var(--fog)',
              border: '1px solid var(--scree)',
              borderRadius: 4,
              fontSize: '0.62rem',
              fontWeight: 600,
              padding: '0 5px',
              lineHeight: '1.5',
              flexShrink: 0,
            }}
          >
            {ticket.size}
          </span>
        )}
      </div>

      {/* Tags + repos row */}
      {(ticket.tags.length > 0 || ticket.repos.length > 0) && (
        <div style={{ marginTop: 6, display: 'flex', flexWrap: 'wrap', gap: 4 }}>
          {ticket.tags.map((tag) => (
            <span
              key={tag}
              data-testid="tag-chip"
              style={{
                fontSize: '0.65rem',
                color: 'var(--fog)',
                border: '1px solid var(--stone)',
                borderRadius: 4,
                padding: '1px 6px',
                background: 'var(--basalt)',
              }}
            >
              {tag}
            </span>
          ))}
          {ticket.repos.map((repo) => (
            <span
              key={repo}
              data-testid="repo-chip"
              style={{
                fontSize: '0.65rem',
                color: 'var(--fog-dim)',
                border: '1px solid var(--stone)',
                borderRadius: 4,
                padding: '1px 6px',
                background: 'var(--basalt)',
                fontFamily: 'monospace',
              }}
            >
              {repo}
            </span>
          ))}
        </div>
      )}

      {/* Move to… control — stop click propagation so card nav doesn't fire */}
      {otherColumns.length > 0 && (
        <div
          style={{ position: 'relative', marginTop: 8, display: 'inline-block' }}
          onClick={(e) => e.stopPropagation()}
        >
          <button
            data-testid="move-to-button"
            onClick={() => setMoveOpen((v) => !v)}
            style={{
              fontSize: '0.72rem',
              color: 'var(--fog)',
              background: 'none',
              border: '1px solid var(--stone)',
              borderRadius: 4,
              padding: '2px 8px',
              cursor: 'pointer',
              fontFamily: 'inherit',
            }}
          >
            Move to…
          </button>

          {moveOpen && (
            <div
              data-testid="move-menu"
              style={{
                position: 'absolute',
                bottom: 'calc(100% + 4px)',
                left: 0,
                background: 'var(--basalt)',
                border: '1px solid var(--stone)',
                borderRadius: 6,
                zIndex: 30,
                minWidth: 150,
                padding: 4,
                boxShadow: '0 4px 16px rgba(0,0,0,0.5)',
              }}
            >
              {otherColumns.map((col) => (
                <button
                  key={col.id}
                  data-testid={`move-to-${col.id}`}
                  onClick={() => handleMove(col.id)}
                  style={{
                    display: 'block',
                    width: '100%',
                    textAlign: 'left',
                    padding: '6px 10px',
                    background: 'none',
                    border: 'none',
                    color: 'var(--chalk)',
                    fontSize: '0.8rem',
                    cursor: 'pointer',
                    fontFamily: 'inherit',
                    borderRadius: 4,
                  }}
                >
                  {col.name}
                </button>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  )
}
