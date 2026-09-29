// BoardView — the kanban board: loads data, subscribes to WS events,
// renders columns (desktop) or ColumnPager (mobile).

import { useEffect, useState, useCallback } from 'react'
import { useParams, useNavigate, Link } from 'react-router-dom'
import { useIsMobile } from '../hooks/useIsMobile'
import { useBoardStore, useBoard } from '../hooks/useBoardStore'
import { useSessionStore } from '../hooks/useSessionStore'
import type { SessionInfo } from '../types'
import * as boardApi from '../lib/boardApi'
import type { Column, Ticket } from '../types'
import BoardFilterBar, { type FilterState } from './BoardFilterBar'
import { applyFilter } from './applyFilter'
import ColumnPager from './ColumnPager'
import TicketCard from './TicketCard'

// ── per-column "add ticket" inline form state ──────────────────────────────────

interface AddTicketState {
  open: boolean
  title: string
}

// ── inline column view (desktop) ──────────────────────────────────────────────

interface ColumnViewProps {
  column: Column
  tickets: Ticket[]
  allColumns: Column[]
  boardId: string
  addState: AddTicketState
  sessions: SessionInfo[]
  onOpenAdd: () => void
  onTitleChange: (v: string) => void
  onSubmit: () => void
  onCancel: () => void
  onRename: (name: string) => void
  onDelete: () => void
  onToggleTerminal: () => void
}

function ColumnView({
  column,
  tickets,
  allColumns,
  addState,
  sessions,
  onOpenAdd,
  onTitleChange,
  onSubmit,
  onCancel,
  onRename,
  onDelete,
  onToggleTerminal,
}: ColumnViewProps) {
  const [renaming, setRenaming] = useState(false)
  const [renameVal, setRenameVal] = useState(column.name)
  const [menuOpen, setMenuOpen] = useState(false)

  async function commitRename() {
    setRenaming(false)
    if (renameVal.trim() && renameVal.trim() !== column.name) {
      onRename(renameVal.trim())
    }
  }

  return (
    <div
      style={{
        width: 280,
        flexShrink: 0,
        display: 'flex',
        flexDirection: 'column',
        background: 'var(--basalt)',
        borderRadius: 8,
        border: '1px solid var(--basalt)',
        maxHeight: '100%',
      }}
    >
      {/* Column header */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 6,
          padding: '10px 12px 8px',
          borderBottom: '1px solid var(--basalt)',
          flexShrink: 0,
        }}
      >
        {/* Column name / rename input */}
        {renaming ? (
          <input
            value={renameVal}
            onChange={(e) => setRenameVal(e.target.value)}
            onBlur={commitRename}
            onKeyDown={(e) => {
              if (e.key === 'Enter') commitRename()
              if (e.key === 'Escape') { setRenaming(false); setRenameVal(column.name) }
            }}
            autoFocus
            style={{
              flex: 1,
              background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
              border: '1px solid var(--blaze)',
              borderRadius: 4,
              padding: '3px 6px',
              color: 'var(--chalk)',
              fontSize: '0.85rem',
              fontFamily: 'inherit',
              outline: 'none',
            }}
          />
        ) : (
          <span
            data-testid={`column-name-${column.id}`}
            onDoubleClick={() => { setRenaming(true); setRenameVal(column.name) }}
            style={{
              flex: 1,
              color: 'var(--chalk)',
              fontWeight: 700,
              fontSize: '0.85rem',
              letterSpacing: '0.05em',
              textTransform: 'uppercase',
              cursor: 'default',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
              whiteSpace: 'nowrap',
            }}
          >
            {column.name}
          </span>
        )}

        {/* Ticket count */}
        <span
          style={{
            fontSize: '0.65rem',
            color: 'var(--fog)',
            background: 'var(--basalt)',
            border: '1px solid var(--stone)',
            borderRadius: 10,
            padding: '0 5px',
            lineHeight: '1.6',
            flexShrink: 0,
          }}
        >
          {tickets.length}
        </span>

        {/* Column actions menu */}
        <div style={{ position: 'relative', flexShrink: 0 }}>
          <button
            data-testid={`column-menu-${column.id}`}
            onClick={() => setMenuOpen((v) => !v)}
            style={{
              background: 'none',
              border: 'none',
              color: 'var(--fog-dim)',
              cursor: 'pointer',
              fontSize: '0.9rem',
              padding: '0 2px',
              fontFamily: 'inherit',
              lineHeight: 1,
            }}
          >
            ⋯
          </button>
          {menuOpen && (
            <div
              style={{
                position: 'absolute',
                top: '100%',
                right: 0,
                background: 'var(--basalt)',
                border: '1px solid var(--stone)',
                borderRadius: 6,
                zIndex: 30,
                minWidth: 140,
                padding: 4,
                boxShadow: '0 4px 16px rgba(0,0,0,0.5)',
              }}
              onClick={() => setMenuOpen(false)}
            >
              <MenuButton onClick={() => { setRenaming(true); setRenameVal(column.name) }}>
                Rename
              </MenuButton>
              <MenuButton onClick={onToggleTerminal}>
                {column.is_terminal ? 'Unmark terminal' : 'Mark as terminal'}
              </MenuButton>
              <MenuButton onClick={onDelete} danger>
                Delete column
              </MenuButton>
            </div>
          )}
        </div>

        {/* Add ticket button */}
        <button
          data-testid={`add-ticket-${column.id}`}
          onClick={onOpenAdd}
          style={{
            background: 'none',
            border: '1px solid var(--stone)',
            borderRadius: 4,
            color: 'var(--fog)',
            cursor: 'pointer',
            fontSize: '0.9rem',
            padding: '1px 6px',
            fontFamily: 'inherit',
            lineHeight: 1.5,
            flexShrink: 0,
          }}
        >
          +
        </button>
      </div>

      {/* Column body: new-ticket form + cards */}
      <div style={{ flex: 1, overflowY: 'auto', padding: '10px 10px 10px' }}>
        {/* Inline add form */}
        {addState.open && (
          <div style={{ marginBottom: 10 }}>
            <input
              data-testid={`new-ticket-title-${column.id}`}
              value={addState.title}
              onChange={(e) => onTitleChange(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') onSubmit()
                if (e.key === 'Escape') onCancel()
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

        {tickets.map((t) => (
          <TicketCard key={t.id} ticket={t} columns={allColumns} sessions={sessions} />
        ))}

        {tickets.length === 0 && !addState.open && (
          <div
            style={{ color: 'var(--basalt)', fontSize: '0.78rem', textAlign: 'center', paddingTop: 20 }}
          >
            Empty
          </div>
        )}
      </div>
    </div>
  )
}

function MenuButton({
  children,
  onClick,
  danger,
}: {
  children: React.ReactNode
  onClick: () => void
  danger?: boolean
}) {
  return (
    <button
      onClick={onClick}
      style={{
        display: 'block',
        width: '100%',
        textAlign: 'left',
        padding: '6px 10px',
        background: 'none',
        border: 'none',
        color: danger ? 'var(--danger)' : 'var(--chalk)',
        fontSize: '0.8rem',
        cursor: 'pointer',
        fontFamily: 'inherit',
        borderRadius: 4,
      }}
    >
      {children}
    </button>
  )
}

// ── Add column inline form ─────────────────────────────────────────────────────

function AddColumnForm({
  boardId,
  onDone,
}: {
  boardId: string
  onDone: () => void
}) {
  const [name, setName] = useState('')
  const [submitting, setSubmitting] = useState(false)

  async function handleSubmit() {
    const trimmed = name.trim()
    if (!trimmed || submitting) return
    setSubmitting(true)
    try {
      await boardApi.addColumn(boardId, { name: trimmed })
      setName('')
      onDone()
    } catch (err) {
      console.error('Failed to add column:', err)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
      <input
        data-testid="add-column-input"
        value={name}
        onChange={(e) => setName(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') handleSubmit()
          if (e.key === 'Escape') onDone()
        }}
        placeholder="Column name"
        autoFocus
        style={{
          background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
          border: '1px solid var(--blaze)',
          borderRadius: 6,
          padding: '5px 8px',
          color: 'var(--chalk)',
          fontSize: '0.85rem',
          fontFamily: 'inherit',
          outline: 'none',
          width: 160,
        }}
      />
      <button
        onClick={handleSubmit}
        disabled={submitting}
        style={{
          background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
          border: '1px solid var(--blaze)',
          borderRadius: 6,
          color: 'var(--amber)',
          padding: '5px 10px',
          fontSize: '0.8rem',
          cursor: 'pointer',
          fontFamily: 'inherit',
        }}
      >
        Add
      </button>
      <button
        onClick={onDone}
        style={{
          background: 'none',
          border: '1px solid var(--stone)',
          borderRadius: 6,
          color: 'var(--fog-dim)',
          padding: '5px 8px',
          fontSize: '0.8rem',
          cursor: 'pointer',
          fontFamily: 'inherit',
        }}
      >
        ✕
      </button>
    </div>
  )
}

// ── BoardView ──────────────────────────────────────────────────────────────────

export default function BoardView() {
  const { id: boardId = '' } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const isMobile = useIsMobile()

  const { board, columns, tickets } = useBoard(boardId)
  const sessions = useSessionStore((s) => s.sessions)
  // Use individual selectors for stable action references (functions never change)
  const subscribeBoard = useBoardStore((s) => s.subscribeBoard)
  const unsubscribeBoard = useBoardStore((s) => s.unsubscribeBoard)
  const setBoardData = useBoardStore((s) => s.setBoardData)

  const [error, setError] = useState<string | null>(null)
  const [assisting, setAssisting] = useState(false)
  const [addColumnOpen, setAddColumnOpen] = useState(false)

  // Per-column add-ticket form state: { [columnId]: { open, title } }
  const [addTicketState, setAddTicketState] = useState<Record<string, AddTicketState>>({})

  // Filter state
  const [filter, setFilter] = useState<FilterState>({ tags: [], priorities: [], sizes: [] })

  // ── Load board + subscribe ─────────────────────────────────────────────────

  // Another board to load: drop the previous board's error before the fetch.
  const [loadingFor, setLoadingFor] = useState('')
  if (boardId && loadingFor !== boardId) {
    setLoadingFor(boardId)
    setError(null)
  }

  useEffect(() => {
    if (!boardId) return

    boardApi
      .getBoard(boardId)
      .then((data) => {
        setBoardData(data, data.columns, data.tickets)
        subscribeBoard(boardId)
      })
      .catch((err: unknown) => {
        setError('Failed to load board: ' + (err instanceof Error ? err.message : String(err)))
      })

    return () => {
      unsubscribeBoard()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [boardId])

  // ── Assist ─────────────────────────────────────────────────────────────────

  async function handleAssist() {
    if (assisting) return
    setAssisting(true)
    try {
      const { session_id } = await boardApi.spawnAssist(boardId, {})
      navigate(`/sessions/${session_id}`)
    } catch (err) {
      console.error('Assist failed:', err)
    } finally {
      setAssisting(false)
    }
  }

  // ── Column editing helpers ─────────────────────────────────────────────────

  async function handleRenameColumn(columnId: string, name: string) {
    try {
      await boardApi.patchColumn(columnId, { name })
    } catch (err) {
      console.error('Failed to rename column:', err)
    }
  }

  async function handleDeleteColumn(columnId: string) {
    if (!confirm('Delete this column? It must be empty.')) return
    try {
      await boardApi.deleteColumn(columnId)
    } catch (err) {
      alert('Failed to delete column: ' + (err instanceof Error ? err.message : String(err)))
    }
  }

  async function handleToggleTerminal(column: Column) {
    try {
      await boardApi.patchColumn(column.id, { isTerminal: !column.is_terminal })
    } catch (err) {
      console.error('Failed to toggle terminal flag:', err)
    }
  }

  // ── Per-column add-ticket helpers ──────────────────────────────────────────

  const openAddTicket = useCallback((columnId: string) => {
    setAddTicketState((prev) => ({
      ...prev,
      [columnId]: { open: true, title: '' },
    }))
  }, [])

  const handleTitleChange = useCallback((columnId: string, title: string) => {
    setAddTicketState((prev) => ({
      ...prev,
      [columnId]: { ...prev[columnId], title },
    }))
  }, [])

  const handleCreateTicket = useCallback(
    async (columnId: string) => {
      const state = addTicketState[columnId]
      if (!state || !state.title.trim()) return
      try {
        await boardApi.createTicket(boardId, {
          title: state.title.trim(),
          columnId,
        })
        setAddTicketState((prev) => ({
          ...prev,
          [columnId]: { open: false, title: '' },
        }))
      } catch (err) {
        console.error('Failed to create ticket:', err)
      }
    },
    [boardId, addTicketState],
  )

  const handleCancelAdd = useCallback((columnId: string) => {
    setAddTicketState((prev) => ({ ...prev, [columnId]: { open: false, title: '' } }))
  }, [])

  // ── Derived data ───────────────────────────────────────────────────────────

  const sortedColumns = [...columns].sort((a, b) => a.rank.localeCompare(b.rank))
  const liveTickets = tickets.filter((t) => !t.archived_at)

  const filteredTickets = applyFilter(liveTickets, filter)

  // Unique filter options from non-archived tickets
  const allTags = [...new Set(liveTickets.flatMap((t) => t.tags))].sort()
  const allPriorities = [...new Set(liveTickets.map((t) => t.priority))].sort()
  const allSizes = [...new Set(liveTickets.map((t) => t.size).filter(Boolean) as string[])].sort()

  function colTickets(colId: string) {
    return filteredTickets
      .filter((t) => t.column_id === colId)
      .sort((a, b) => a.rank.localeCompare(b.rank))
  }

  // ── Render ─────────────────────────────────────────────────────────────────

  if (error) {
    return (
      <div
        style={{
          padding: 24,
          color: 'var(--danger)',
          background: 'var(--basalt)',
          minHeight: '100vh',
        }}
      >
        {error}
      </div>
    )
  }

  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        height: '100vh',
        background: 'var(--basalt)',
        color: 'var(--chalk)',
        overflow: 'hidden',
      }}
    >
      {/* ── Header ─────────────────────────────────────────────────────────── */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 10,
          padding: '10px 16px',
          borderBottom: '1px solid var(--stone)',
          background: 'var(--basalt)',
          flexShrink: 0,
          flexWrap: 'wrap',
        }}
      >
        {/* Back button */}
        <button
          onClick={() => navigate('/boards')}
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

        {/* Board name */}
        <span
          style={{
            color: 'var(--blaze)',
            fontWeight: 700,
            fontSize: '1rem',
            letterSpacing: '0.08em',
            textTransform: 'uppercase',
            flex: 1,
            minWidth: 0,
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
          }}
        >
          {board?.name ?? '…'}
        </span>

        {/* Add column */}
        {addColumnOpen ? (
          <AddColumnForm boardId={boardId} onDone={() => setAddColumnOpen(false)} />
        ) : (
          <button
            data-testid="add-column-button"
            onClick={() => setAddColumnOpen(true)}
            style={{
              background: 'none',
              border: '1px solid var(--stone)',
              borderRadius: 6,
              color: 'var(--fog)',
              padding: '4px 10px',
              fontSize: '0.8rem',
              cursor: 'pointer',
              fontFamily: 'inherit',
            }}
          >
            + Column
          </button>
        )}

        {/* Archive link */}
        <Link
          to={`/boards/${boardId}/archive`}
          data-testid="archive-link"
          style={{
            color: 'var(--fog-dim)',
            fontSize: '0.8rem',
            textDecoration: 'none',
            border: '1px solid var(--stone)',
            borderRadius: 6,
            padding: '4px 10px',
            letterSpacing: '0.03em',
          }}
        >
          Archive
        </Link>

        {/* Assist button */}
        <button
          data-testid="assist-button"
          onClick={handleAssist}
          disabled={assisting}
          style={{
            background: assisting ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
            color: assisting ? 'var(--stone)' : 'var(--amber)',
            border: '1px solid var(--blaze)',
            borderRadius: 6,
            padding: '5px 14px',
            fontWeight: 600,
            cursor: assisting ? 'default' : 'pointer',
            fontSize: '0.85rem',
            letterSpacing: '0.05em',
            fontFamily: 'inherit',
          }}
        >
          {assisting ? 'Starting…' : 'Assist'}
        </button>
      </div>

      {/* ── Filter bar ─────────────────────────────────────────────────────── */}
      <BoardFilterBar
        availableTags={allTags}
        availablePriorities={allPriorities}
        availableSizes={allSizes}
        filter={filter}
        onChange={setFilter}
      />

      {/* ── Column area ────────────────────────────────────────────────────── */}
      {isMobile ? (
        <ColumnPager
          columns={sortedColumns}
          tickets={filteredTickets}
          sessions={sessions}
          filter={filter}
          boardId={boardId}
          onAddTicket={openAddTicket}
          addTicketState={addTicketState}
          onNewTicketTitleChange={handleTitleChange}
          onNewTicketSubmit={handleCreateTicket}
          onNewTicketCancel={handleCancelAdd}
        />
      ) : (
        <div
          style={{
            display: 'flex',
            gap: 12,
            padding: '12px 16px',
            overflowX: 'auto',
            flex: 1,
            alignItems: 'flex-start',
          }}
        >
          {sortedColumns.map((col) => (
            <ColumnView
              key={col.id}
              column={col}
              tickets={colTickets(col.id)}
              allColumns={sortedColumns}
              boardId={boardId}
              addState={addTicketState[col.id] ?? { open: false, title: '' }}
              sessions={sessions}
              onOpenAdd={() => openAddTicket(col.id)}
              onTitleChange={(v) => handleTitleChange(col.id, v)}
              onSubmit={() => handleCreateTicket(col.id)}
              onCancel={() => handleCancelAdd(col.id)}
              onRename={(name) => handleRenameColumn(col.id, name)}
              onDelete={() => handleDeleteColumn(col.id)}
              onToggleTerminal={() => handleToggleTerminal(col)}
            />
          ))}
        </div>
      )}
    </div>
  )
}
