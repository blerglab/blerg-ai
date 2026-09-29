// TicketDetail — full ticket editing view.
// Route: /boards/:id/ticket/:ticketId

import { useEffect, useState } from 'react'
import { useParams, useNavigate, Link } from 'react-router-dom'
import { useBoardStore, useBoard } from '../hooks/useBoardStore'
import * as boardApi from '../lib/boardApi'
import { ApiError } from '../lib/boardApi'
import type { TicketDetail as TicketDetailData, TicketEvent } from '../types'

// ── constants ─────────────────────────────────────────────────────────────────

const PRIORITIES = ['urgent', 'high', 'medium', 'low']
const SIZES = ['xs', 's', 'm', 'l', 'xl']

// ── styles ────────────────────────────────────────────────────────────────────

const S = {
  page: {
    minHeight: '100vh',
    background: 'var(--basalt)',
    color: 'var(--chalk)',
    fontFamily: 'inherit',
  } as React.CSSProperties,

  header: {
    display: 'flex',
    alignItems: 'center',
    gap: 10,
    padding: '10px 16px',
    borderBottom: '1px solid var(--stone)',
    background: 'var(--basalt)',
    flexShrink: 0,
    flexWrap: 'wrap' as const,
  } as React.CSSProperties,

  body: {
    maxWidth: 720,
    margin: '0 auto',
    padding: '20px 16px 60px',
  } as React.CSSProperties,

  section: {
    marginBottom: 24,
  } as React.CSSProperties,

  label: {
    display: 'block',
    fontSize: '0.72rem',
    color: 'var(--fog)',
    letterSpacing: '0.08em',
    textTransform: 'uppercase' as const,
    marginBottom: 6,
    fontWeight: 700,
  } as React.CSSProperties,

  textarea: {
    width: '100%',
    background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
    border: '1px solid var(--stone)',
    borderRadius: 6,
    padding: '10px 12px',
    color: 'var(--chalk)',
    fontSize: '0.9rem',
    fontFamily: 'inherit',
    resize: 'vertical' as const,
    minHeight: 120,
    boxSizing: 'border-box' as const,
    outline: 'none',
  } as React.CSSProperties,

  select: {
    background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
    border: '1px solid var(--stone)',
    borderRadius: 6,
    padding: '6px 10px',
    color: 'var(--chalk)',
    fontSize: '0.88rem',
    fontFamily: 'inherit',
    outline: 'none',
    cursor: 'pointer',
  } as React.CSSProperties,

  input: {
    background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
    border: '1px solid var(--stone)',
    borderRadius: 6,
    padding: '6px 10px',
    color: 'var(--chalk)',
    fontSize: '0.88rem',
    fontFamily: 'inherit',
    outline: 'none',
  } as React.CSSProperties,

  primaryBtn: {
    background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
    border: '1px solid var(--blaze)',
    borderRadius: 6,
    color: 'var(--amber)',
    padding: '6px 14px',
    fontWeight: 600,
    cursor: 'pointer',
    fontSize: '0.85rem',
    fontFamily: 'inherit',
  } as React.CSSProperties,

  ghostBtn: {
    background: 'none',
    border: '1px solid var(--stone)',
    borderRadius: 6,
    color: 'var(--fog)',
    padding: '5px 10px',
    cursor: 'pointer',
    fontSize: '0.8rem',
    fontFamily: 'inherit',
  } as React.CSSProperties,

  dangerBtn: {
    background: 'none',
    border: '1px solid color-mix(in srgb, var(--danger) 35%, var(--scree))',
    borderRadius: 6,
    color: 'var(--danger)',
    padding: '5px 10px',
    cursor: 'pointer',
    fontSize: '0.8rem',
    fontFamily: 'inherit',
  } as React.CSSProperties,

  chip: {
    display: 'inline-flex',
    alignItems: 'center',
    gap: 4,
    fontSize: '0.72rem',
    color: 'var(--fog)',
    border: '1px solid var(--stone)',
    borderRadius: 4,
    padding: '2px 8px',
    background: 'var(--basalt)',
  } as React.CSSProperties,

  repoChip: {
    display: 'inline-flex',
    alignItems: 'center',
    gap: 4,
    fontSize: '0.72rem',
    color: 'var(--fog-dim)',
    border: '1px solid var(--stone)',
    borderRadius: 4,
    padding: '2px 8px',
    background: 'var(--basalt)',
    fontFamily: 'monospace',
  } as React.CSSProperties,

  chipRemove: {
    background: 'none',
    border: 'none',
    color: 'var(--fog-dim)',
    cursor: 'pointer',
    fontSize: '0.8rem',
    padding: '0 0 0 2px',
    lineHeight: 1,
    fontFamily: 'inherit',
  } as React.CSSProperties,

  archivedBanner: {
    background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
    border: '1px solid color-mix(in srgb, var(--blaze) 35%, var(--scree))',
    borderRadius: 6,
    padding: '10px 14px',
    color: 'var(--blaze)',
    fontSize: '0.88rem',
    marginBottom: 20,
    fontWeight: 600,
  } as React.CSSProperties,

  staleNotice: {
    background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
    border: '1px solid var(--blaze)',
    borderRadius: 6,
    padding: '8px 12px',
    color: 'var(--blaze)',
    fontSize: '0.82rem',
    marginBottom: 12,
  } as React.CSSProperties,

  eventItem: {
    padding: '8px 0',
    borderBottom: '1px solid var(--basalt)',
    fontSize: '0.82rem',
    color: 'var(--fog)',
  } as React.CSSProperties,

  depRow: {
    display: 'flex',
    alignItems: 'center',
    gap: 8,
    padding: '4px 0',
  } as React.CSSProperties,
} as const

// ── helpers ───────────────────────────────────────────────────────────────────

function formatEventTime(iso: string): string {
  try {
    return new Date(iso).toLocaleString()
  } catch {
    return iso
  }
}

function eventLabel(ev: TicketEvent): string {
  const labels: Record<string, string> = {
    created: 'Created',
    updated: 'Updated',
    moved: 'Moved',
    archived: 'Archived',
    split: 'Split',
    dependency_added: 'Dependency added',
    dependency_removed: 'Dependency removed',
  }
  return labels[ev.type] ?? ev.type
}

// ── TicketDetail ──────────────────────────────────────────────────────────────

export default function TicketDetail() {
  const { id: boardId = '', ticketId = '' } = useParams<{ id: string; ticketId: string }>()
  const navigate = useNavigate()

  // ── ticket state ──────────────────────────────────────────────────────────
  const [ticket, setTicket] = useState<TicketDetailData | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [staleNotice, setStaleNotice] = useState(false)

  // ── body editor state ─────────────────────────────────────────────────────
  const [bodyValue, setBodyValue] = useState('')
  const [bodySaving, setBodySaving] = useState(false)

  // ── tags editor state ─────────────────────────────────────────────────────
  const [newTag, setNewTag] = useState('')

  // ── dependency state ──────────────────────────────────────────────────────
  const [newDepId, setNewDepId] = useState('')

  // ── split state ───────────────────────────────────────────────────────────
  const [splitTitles, setSplitTitles] = useState<string[]>(['', ''])
  const [splitting, setSplitting] = useState(false)

  // ── move state ────────────────────────────────────────────────────────────
  const [moveOpen, setMoveOpen] = useState(false)
  const [moving, setMoving] = useState(false)

  // ── activity feed state ───────────────────────────────────────────────────
  const [events, setEvents] = useState<TicketEvent[]>([])
  const [eventsCursor, setEventsCursor] = useState('')
  const [eventsLoadingMore, setEventsLoadingMore] = useState(false)

  // ── board store for resolving titles / columns ────────────────────────────
  const { columns, tickets: boardTickets } = useBoard(boardId)
  const addPendingOp = useBoardStore((s) => s.addPendingOp)

  const otherColumns = columns.filter((c) => c.id !== ticket?.column_id)

  // ── load ticket ───────────────────────────────────────────────────────────
  // Another ticket to load: drop the previous ticket's error before the fetch.
  const [loadingFor, setLoadingFor] = useState('')
  if (ticketId && loadingFor !== ticketId) {
    setLoadingFor(ticketId)
    setLoadError(null)
  }

  useEffect(() => {
    if (!ticketId) return
    boardApi
      .getTicket(ticketId)
      .then((t) => {
        setTicket(t)
        setBodyValue(t.body ?? '')
      })
      .catch((err: unknown) => {
        setLoadError(err instanceof Error ? err.message : String(err))
      })
  }, [ticketId])

  // ── load events ───────────────────────────────────────────────────────────
  useEffect(() => {
    if (!ticketId) return
    boardApi
      .listTicketEvents(ticketId, {})
      .then((r) => {
        setEvents(r.events)
        setEventsCursor(r.next_cursor)
      })
      .catch(() => {
        // best-effort; don't block the view
      })
  }, [ticketId])

  // ── stale (409) handler ─────────────────────────────────────────────────────
  // On an optimistic-concurrency conflict, refetch the authoritative ticket and
  // surface a brief "changed elsewhere, reloaded" notice. Returns true if the
  // error was a 409 (and was handled), false otherwise.
  async function handleStale(err: unknown): Promise<boolean> {
    if (err instanceof ApiError && err.status === 409) {
      const fresh = await boardApi.getTicket(ticketId)
      setTicket(fresh)
      setBodyValue(fresh.body ?? '')
      setStaleNotice(true)
      setTimeout(() => setStaleNotice(false), 4000)
      return true
    }
    return false
  }

  // ── body save ─────────────────────────────────────────────────────────────
  async function handleBodySave() {
    if (!ticket || bodyValue === (ticket.body ?? '')) return
    setBodySaving(true)
    try {
      const updated = await boardApi.updateTicket(ticket.id, {
        body: bodyValue,
        expectVersion: ticket.version,
      })
      setTicket((prev) => (prev ? { ...prev, ...updated } : null))
    } catch (err) {
      await handleStale(err)
    } finally {
      setBodySaving(false)
    }
  }

  // ── priority change ───────────────────────────────────────────────────────
  async function handlePriorityChange(priority: string) {
    if (!ticket) return
    try {
      const updated = await boardApi.updateTicket(ticket.id, { priority, expectVersion: ticket.version })
      setTicket((prev) => (prev ? { ...prev, ...updated } : null))
    } catch (err) {
      await handleStale(err)
    }
  }

  // ── size change ───────────────────────────────────────────────────────────
  async function handleSizeChange(size: string) {
    if (!ticket) return
    try {
      const updated = await boardApi.updateTicket(ticket.id, {
        size: size === '' ? null : size,
        expectVersion: ticket.version,
      })
      setTicket((prev) => (prev ? { ...prev, ...updated } : null))
    } catch (err) {
      await handleStale(err)
    }
  }

  // ── tag add ───────────────────────────────────────────────────────────────
  async function handleAddTag() {
    const tag = newTag.trim()
    if (!tag || !ticket) return
    setNewTag('')
    try {
      const updated = await boardApi.updateTicket(ticket.id, {
        addTags: [tag],
        expectVersion: ticket.version,
      })
      setTicket((prev) => (prev ? { ...prev, ...updated, tags: updated.tags } : null))
    } catch (err) {
      await handleStale(err)
    }
  }

  // ── tag remove ────────────────────────────────────────────────────────────
  async function handleRemoveTag(tag: string) {
    if (!ticket) return
    try {
      const updated = await boardApi.updateTicket(ticket.id, {
        rmTags: [tag],
        expectVersion: ticket.version,
      })
      setTicket((prev) => (prev ? { ...prev, ...updated, tags: updated.tags } : null))
    } catch (err) {
      await handleStale(err)
    }
  }

  // ── add dependency ────────────────────────────────────────────────────────
  async function handleAddDep() {
    const depId = newDepId.trim()
    if (!depId || !ticket) return
    setNewDepId('')
    try {
      await boardApi.addDependency(ticket.id, { dependsOnTicketId: depId })
      // Refetch to get updated depends_on list
      const fresh = await boardApi.getTicket(ticketId)
      setTicket(fresh)
      setBodyValue(fresh.body ?? '')
    } catch {
      // ignore
    }
  }

  // ── remove dependency ─────────────────────────────────────────────────────
  async function handleRemoveDep(depId: string) {
    if (!ticket) return
    try {
      await boardApi.removeDependency(ticket.id, depId)
      // Refetch to get updated depends_on list
      const fresh = await boardApi.getTicket(ticketId)
      setTicket(fresh)
      setBodyValue(fresh.body ?? '')
    } catch {
      // ignore
    }
  }

  // ── move ──────────────────────────────────────────────────────────────────
  async function handleMove(columnId: string) {
    if (!ticket) return
    setMoveOpen(false)
    setMoving(true)
    const opId = boardApi.newOpId()
    addPendingOp(opId)
    try {
      const updated = await boardApi.moveTicket(ticket.id, { columnId, opId })
      setTicket((prev) => (prev ? { ...prev, ...updated } : null))
    } catch {
      // ignore
    } finally {
      setMoving(false)
    }
  }

  // ── archive ───────────────────────────────────────────────────────────────
  async function handleArchive() {
    if (!ticket) return
    try {
      const updated = await boardApi.archiveTicket(ticket.id)
      setTicket((prev) => (prev ? { ...prev, ...updated } : null))
    } catch {
      // ignore
    }
  }

  // ── split ─────────────────────────────────────────────────────────────────
  const splitValid = splitTitles.filter((t) => t.trim()).length >= 2

  async function handleSplit() {
    if (!ticket || !splitValid || splitting) return
    setSplitting(true)
    const titles = splitTitles.filter((t) => t.trim())
    try {
      await boardApi.splitTicket(ticket.id, { titles })
      // Origin ticket is now archived — refetch
      const fresh = await boardApi.getTicket(ticketId)
      setTicket(fresh)
      setBodyValue(fresh.body ?? '')
      setSplitTitles(['', ''])
    } catch {
      // ignore
    } finally {
      setSplitting(false)
    }
  }

  // ── load more events ──────────────────────────────────────────────────────
  async function handleLoadMoreEvents() {
    if (!eventsCursor || eventsLoadingMore) return
    setEventsLoadingMore(true)
    try {
      const r = await boardApi.listTicketEvents(ticketId, { cursor: eventsCursor })
      setEvents((prev) => [...prev, ...r.events])
      setEventsCursor(r.next_cursor)
    } catch {
      // ignore
    } finally {
      setEventsLoadingMore(false)
    }
  }

  // ── helpers for dep title resolution ─────────────────────────────────────
  function resolveTitle(id: string): string {
    const t = boardTickets.find((bt) => bt.id === id)
    return t ? t.title : id
  }

  // ── render ────────────────────────────────────────────────────────────────

  if (loadError) {
    return (
      <div style={{ ...S.page, padding: 24, color: 'var(--danger)' }}>
        {loadError}
        <br />
        <button
          style={{ ...S.ghostBtn, marginTop: 12 }}
          onClick={() => navigate(`/boards/${boardId}`)}
        >
          Back to board
        </button>
      </div>
    )
  }

  return (
    <div style={S.page}>
      {/* ── Header ─────────────────────────────────────────────────────── */}
      <div style={S.header}>
        <button
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
            letterSpacing: '0.06em',
            flex: 1,
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
          }}
        >
          {ticket?.title ?? '…'}
        </span>
      </div>

      {/* ── Body ───────────────────────────────────────────────────────── */}
      <div style={S.body}>
        {/* Stale concurrency notice */}
        {staleNotice && (
          <div style={S.staleNotice} data-testid="stale-notice">
            Changed elsewhere — reloaded with latest version.
          </div>
        )}

        {/* Archived banner */}
        {ticket?.archived_at && (
          <div style={S.archivedBanner} data-testid="archived-banner">
            Archived {new Date(ticket.archived_at).toLocaleString()}
          </div>
        )}

        {/* Session link */}
        {ticket?.session_id && (
          <div style={{ marginBottom: 16 }}>
            <Link
              to={`/sessions/${ticket.session_id}`}
              data-testid="session-link"
              style={{ color: 'var(--blaze)', fontSize: '0.85rem', textDecoration: 'none' }}
            >
              → Bound Assist session
            </Link>
          </div>
        )}

        {/* ── Repo chips ────────────────────────────────────────────────── */}
        {ticket && ticket.repos.length > 0 && (
          <div style={{ ...S.section, display: 'flex', flexWrap: 'wrap', gap: 6 }}>
            {ticket.repos.map((r) => (
              <span key={r} style={S.repoChip} data-testid="repo-chip">
                {r}
              </span>
            ))}
          </div>
        )}

        {/* ── Body editor ───────────────────────────────────────────────── */}
        <div style={S.section}>
          <label style={S.label}>Body</label>
          <textarea
            data-testid="body-textarea"
            value={bodyValue}
            onChange={(e) => setBodyValue(e.target.value)}
            style={S.textarea}
            rows={6}
            placeholder="Ticket description…"
          />
          <div style={{ marginTop: 6, display: 'flex', gap: 6 }}>
            <button
              data-testid="save-body-button"
              onClick={handleBodySave}
              disabled={bodySaving || !ticket || bodyValue === (ticket.body ?? '')}
              style={{
                ...S.primaryBtn,
                opacity: bodySaving || !ticket || bodyValue === (ticket.body ?? '') ? 0.5 : 1,
                cursor: bodySaving ? 'default' : 'pointer',
              }}
            >
              {bodySaving ? 'Saving…' : 'Save'}
            </button>
          </div>
        </div>

        {/* ── Metadata row ──────────────────────────────────────────────── */}
        <div style={{ ...S.section, display: 'flex', gap: 16, flexWrap: 'wrap' }}>
          {/* Priority */}
          <div>
            <label style={S.label}>Priority</label>
            <select
              data-testid="priority-select"
              value={ticket?.priority ?? 'medium'}
              onChange={(e) => handlePriorityChange(e.target.value)}
              style={S.select}
              disabled={!ticket}
            >
              {PRIORITIES.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </select>
          </div>

          {/* Size */}
          <div>
            <label style={S.label}>Size</label>
            <select
              data-testid="size-select"
              value={ticket?.size ?? ''}
              onChange={(e) => handleSizeChange(e.target.value)}
              style={S.select}
              disabled={!ticket}
            >
              <option value="">—</option>
              {SIZES.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          </div>
        </div>

        {/* ── Tags editor ───────────────────────────────────────────────── */}
        <div style={S.section}>
          <label style={S.label}>Tags</label>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, marginBottom: 8 }}>
            {(ticket?.tags ?? []).map((tag) => (
              <span key={tag} style={S.chip} data-testid={`tag-chip-${tag}`}>
                {tag}
                <button
                  data-testid={`remove-tag-${tag}`}
                  onClick={() => handleRemoveTag(tag)}
                  style={S.chipRemove}
                  title={`Remove tag ${tag}`}
                >
                  ×
                </button>
              </span>
            ))}
          </div>
          <div style={{ display: 'flex', gap: 6 }}>
            <input
              data-testid="add-tag-input"
              value={newTag}
              onChange={(e) => setNewTag(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') handleAddTag() }}
              placeholder="Add tag…"
              style={{ ...S.input, width: 140 }}
              disabled={!ticket}
            />
            <button
              data-testid="add-tag-button"
              onClick={handleAddTag}
              style={S.ghostBtn}
              disabled={!newTag.trim() || !ticket}
            >
              Add
            </button>
          </div>
        </div>

        {/* ── Dependencies ──────────────────────────────────────────────── */}
        <div style={S.section}>
          <label style={S.label}>Depends on</label>
          {(ticket?.depends_on ?? []).map((depId) => (
            <div key={depId} style={S.depRow}>
              <Link
                to={`/boards/${boardId}/ticket/${depId}`}
                style={{ color: 'var(--fog)', fontSize: '0.85rem', textDecoration: 'none', flex: 1 }}
              >
                {resolveTitle(depId)}
              </Link>
              <button
                data-testid={`remove-dep-${depId}`}
                onClick={() => handleRemoveDep(depId)}
                style={S.chipRemove}
                title="Remove dependency"
              >
                ×
              </button>
            </div>
          ))}
          <div style={{ display: 'flex', gap: 6, marginTop: 6 }}>
            <input
              data-testid="add-dep-input"
              value={newDepId}
              onChange={(e) => setNewDepId(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') handleAddDep() }}
              placeholder="Ticket ID…"
              style={{ ...S.input, width: 180 }}
              disabled={!ticket}
            />
            <button
              data-testid="add-dep-button"
              onClick={handleAddDep}
              style={S.ghostBtn}
              disabled={!newDepId.trim() || !ticket}
            >
              Add
            </button>
          </div>
        </div>

        {/* Blocks */}
        {ticket && ticket.blocks.length > 0 && (
          <div style={S.section}>
            <label style={S.label}>Blocks</label>
            {ticket.blocks.map((blockId) => (
              <div key={blockId} style={S.depRow}>
                <Link
                  to={`/boards/${boardId}/ticket/${blockId}`}
                  style={{ color: 'var(--fog)', fontSize: '0.85rem', textDecoration: 'none' }}
                >
                  {resolveTitle(blockId)}
                </Link>
              </div>
            ))}
          </div>
        )}

        {/* ── Actions ───────────────────────────────────────────────────── */}
        <div style={{ ...S.section, display: 'flex', gap: 8, flexWrap: 'wrap' }}>
          {/* Move to… */}
          <div style={{ position: 'relative' }}>
            <button
              data-testid="move-to-button"
              onClick={() => setMoveOpen((v) => !v)}
              style={S.ghostBtn}
              disabled={!ticket || moving || otherColumns.length === 0}
            >
              {moving ? 'Moving…' : 'Move to…'}
            </button>
            {moveOpen && (
              <div
                style={{
                  position: 'absolute',
                  top: 'calc(100% + 4px)',
                  left: 0,
                  background: 'var(--basalt)',
                  border: '1px solid var(--stone)',
                  borderRadius: 6,
                  zIndex: 30,
                  minWidth: 160,
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
                      fontSize: '0.85rem',
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

          {/* Archive */}
          {!ticket?.archived_at && (
            <button
              data-testid="archive-button"
              onClick={handleArchive}
              style={S.dangerBtn}
              disabled={!ticket}
            >
              Archive
            </button>
          )}
        </div>

        {/* ── Split section ─────────────────────────────────────────────── */}
        <div style={S.section}>
          <label style={S.label}>Split into</label>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6, marginBottom: 8 }}>
            {splitTitles.map((title, i) => (
              <input
                key={i}
                data-testid={`split-title-${i}`}
                value={title}
                onChange={(e) => {
                  const next = [...splitTitles]
                  next[i] = e.target.value
                  setSplitTitles(next)
                }}
                placeholder={`Child ${i + 1} title…`}
                style={S.input}
                disabled={splitting}
              />
            ))}
          </div>
          <div style={{ display: 'flex', gap: 6 }}>
            <button
              onClick={() => setSplitTitles((prev) => [...prev, ''])}
              style={S.ghostBtn}
              disabled={splitting}
            >
              + Title
            </button>
            <button
              data-testid="split-button"
              onClick={handleSplit}
              disabled={!splitValid || splitting || !ticket}
              style={{
                ...S.primaryBtn,
                opacity: !splitValid || splitting ? 0.4 : 1,
                cursor: !splitValid ? 'not-allowed' : 'pointer',
              }}
            >
              {splitting ? 'Splitting…' : 'Split'}
            </button>
          </div>
        </div>

        {/* ── Activity feed ─────────────────────────────────────────────── */}
        <div style={S.section}>
          <label style={S.label}>Activity</label>
          {events.length === 0 && (
            <div style={{ color: 'var(--stone)', fontSize: '0.82rem' }}>No events yet.</div>
          )}
          <div>
            {events.map((ev) => (
              <div key={ev.id} data-testid={`event-${ev.id}`} style={S.eventItem}>
                <span style={{ color: 'var(--blaze)', fontWeight: 600 }}>{eventLabel(ev)}</span>
                {' '}
                <span style={{ color: 'var(--fog-dim)' }}>by {ev.actor}</span>
                {' · '}
                <span style={{ color: 'var(--stone)' }}>{formatEventTime(ev.created_at)}</span>
              </div>
            ))}
          </div>
          {eventsCursor && (
            <button
              data-testid="load-more-events"
              onClick={handleLoadMoreEvents}
              disabled={eventsLoadingMore}
              style={{ ...S.ghostBtn, marginTop: 10 }}
            >
              {eventsLoadingMore ? 'Loading…' : 'Load more'}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
