// ColumnPager — mobile column view: one lane visible at a time, tab between them.

import { useState } from 'react'
import type { Column, Ticket, SessionInfo } from '../types'
import type { FilterState } from './BoardFilterBar'
import { applyFilter } from './applyFilter'
import TicketCard from './TicketCard'

interface ColumnPagerProps {
  columns: Column[]
  tickets: Ticket[]
  sessions: SessionInfo[]
  filter: FilterState
  boardId: string
  onAddTicket: (columnId: string) => void
  addTicketState: Record<string, { open: boolean; title: string }>
  onNewTicketTitleChange: (columnId: string, title: string) => void
  onNewTicketSubmit: (columnId: string) => void
  onNewTicketCancel: (columnId: string) => void
}

export default function ColumnPager({
  columns,
  tickets,
  sessions,
  filter,
  onAddTicket,
  addTicketState,
  onNewTicketTitleChange,
  onNewTicketSubmit,
  onNewTicketCancel,
}: ColumnPagerProps) {
  const [activeIndex, setActiveIndex] = useState(0)

  // Sort columns by rank lexicographically
  const sortedCols = [...columns].sort((a, b) => a.rank.localeCompare(b.rank))

  if (sortedCols.length === 0) return null

  const safeIndex = Math.min(activeIndex, sortedCols.length - 1)
  const activeCol = sortedCols[safeIndex]

  // Compute per-column ticket counts and blocked counts for tab badges
  const allFilteredByCol = (colId: string) => {
    const colTickets = tickets.filter((t) => t.column_id === colId && !t.archived_at)
    return applyFilter(colTickets, filter)
  }

  const hasAttention = (colId: string) =>
    tickets.some((t) => t.column_id === colId && !t.archived_at && (t.blocked || t.session_id))

  const colTickets = allFilteredByCol(activeCol.id)
  const state = addTicketState[activeCol.id] ?? { open: false, title: '' }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
      {/* Tab row */}
      <div
        style={{
          display: 'flex',
          overflowX: 'auto',
          borderBottom: '1px solid var(--stone)',
          background: 'var(--basalt)',
          gap: 0,
          flexShrink: 0,
        }}
      >
        {sortedCols.map((col, i) => {
          const count = tickets.filter((t) => t.column_id === col.id && !t.archived_at).length
          const attention = hasAttention(col.id)
          const isActive = i === safeIndex
          return (
            <button
              key={col.id}
              data-testid={`column-tab-${col.id}`}
              onClick={() => setActiveIndex(i)}
              style={{
                flex: '0 0 auto',
                padding: '10px 14px',
                background: 'none',
                border: 'none',
                borderBottom: isActive ? '2px solid var(--blaze)' : '2px solid transparent',
                color: isActive ? 'var(--chalk)' : 'var(--fog-dim)',
                fontSize: '0.85rem',
                fontWeight: isActive ? 700 : 400,
                cursor: 'pointer',
                fontFamily: 'inherit',
                display: 'flex',
                alignItems: 'center',
                gap: 6,
                whiteSpace: 'nowrap',
              }}
            >
              {col.name}
              {count > 0 && (
                <span
                  style={{
                    fontSize: '0.65rem',
                    background: isActive ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'var(--basalt)',
                    color: isActive ? 'var(--amber)' : 'var(--fog)',
                    border: `1px solid ${isActive ? 'var(--blaze)' : 'var(--stone)'}`,
                    borderRadius: 10,
                    padding: '0 5px',
                    lineHeight: '1.4',
                  }}
                >
                  {count}
                </span>
              )}
              {attention && (
                <span
                  style={{
                    width: 6,
                    height: 6,
                    borderRadius: '50%',
                    background: 'var(--blaze)',
                    display: 'inline-block',
                    flexShrink: 0,
                  }}
                />
              )}
            </button>
          )
        })}
      </div>

      {/* Active column tickets */}
      <div style={{ flex: 1, overflowY: 'auto', padding: '12px 16px' }}>
        {/* Column header row */}
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            marginBottom: 10,
          }}
        >
          <span style={{ fontSize: '0.8rem', color: 'var(--fog)' }}>
            {colTickets.length} ticket{colTickets.length !== 1 ? 's' : ''}
          </span>
          <button
            data-testid={`add-ticket-${activeCol.id}`}
            onClick={() => onAddTicket(activeCol.id)}
            style={{
              fontSize: '1rem',
              color: 'var(--fog)',
              background: 'none',
              border: '1px solid var(--stone)',
              borderRadius: 4,
              padding: '1px 8px',
              cursor: 'pointer',
              fontFamily: 'inherit',
              lineHeight: 1.5,
            }}
          >
            +
          </button>
        </div>

        {/* New ticket form */}
        {state.open && (
          <div style={{ marginBottom: 10 }}>
            <input
              data-testid={`new-ticket-title-${activeCol.id}`}
              value={state.title}
              onChange={(e) => onNewTicketTitleChange(activeCol.id, e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') onNewTicketSubmit(activeCol.id)
                if (e.key === 'Escape') onNewTicketCancel(activeCol.id)
              }}
              placeholder="Ticket title"
              autoFocus
              style={{
                width: '100%',
                background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
                border: '1px solid var(--blaze)',
                borderRadius: 6,
                padding: '7px 10px',
                color: 'var(--chalk)',
                fontSize: '0.88rem',
                fontFamily: 'inherit',
                boxSizing: 'border-box',
                outline: 'none',
              }}
            />
          </div>
        )}

        {colTickets.map((t) => (
          <TicketCard key={t.id} ticket={t} columns={sortedCols} sessions={sessions} />
        ))}

        {colTickets.length === 0 && !state.open && (
          <div style={{ color: 'var(--stone)', fontSize: '0.8rem', textAlign: 'center', paddingTop: 24 }}>
            No tickets
          </div>
        )}
      </div>
    </div>
  )
}
