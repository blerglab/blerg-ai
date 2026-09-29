// CapabilitiesPanel: what a session has loaded — skills, plugins, MCP
// servers, slash commands, tools, subagents — from its latest capabilities
// report. Driven only by the report's data (no per-engine branches): a group
// the report doesn't carry says "not reported by this engine". A modal on
// desktop, a full-screen sheet on narrow screens (CSS). All text renders as
// plain text.
import { useEffect, useId, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import type { CapabilityGroup, CapabilityItem } from '../types'
import { CAPABILITY_GROUPS, capabilityCount, matchesQuery, type CapabilitiesReport } from '../lib/capabilities'
import { useBackdropClose } from '../hooks/useBackdropClose'
import './CapabilitiesPanel.css'

// Groups longer than this start collapsed.
const COLLAPSE_OVER = 15
// Descriptions longer than this are cut, with a toggle to read the rest.
const DESC_PREVIEW = 160

interface Props {
  report: CapabilitiesReport | null
  // When set, capabilities can't be reported for this session at all (a
  // terminal session); this says why, instead of the "nothing yet" state.
  unavailable?: string
  onClose: () => void
}

const FOCUSABLE = 'button:not([disabled]), input:not([disabled]), [href], [tabindex]:not([tabindex="-1"])'

export default function CapabilitiesPanel({ report, unavailable, onClose }: Props) {
  const titleId = useId()
  const dialogRef = useRef<HTMLDivElement | null>(null)
  const backdrop = useBackdropClose(onClose)
  const searchRef = useRef<HTMLInputElement | null>(null)
  const [query, setQuery] = useState('')
  const payload = report?.payload ?? null
  // Groups the user opened or closed; the rest follow their default (long
  // groups start closed), which also holds for a report that arrives later.
  const [openState, setOpenState] = useState<Record<string, boolean>>({})

  // Focus moves into the dialog on open and back to whatever had it on close.
  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null
    const first = searchRef.current ?? dialogRef.current?.querySelector<HTMLElement>(FOCUSABLE)
    first?.focus()
    return () => opener?.focus?.()
  }, [])

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === 'Escape') {
      e.stopPropagation()
      onClose()
      return
    }
    if (e.key !== 'Tab' || !dialogRef.current) return
    const nodes = Array.from(dialogRef.current.querySelectorAll<HTMLElement>(FOCUSABLE))
    if (nodes.length === 0) return
    const first = nodes[0]
    const last = nodes[nodes.length - 1]
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault()
      first.focus()
    }
  }

  const q = query.trim()
  const isOpen = (g: CapabilityGroup) => openState[g.id] ?? g.items.length <= COLLAPSE_OVER
  const toggle = (g: CapabilityGroup) => setOpenState(prev => ({ ...prev, [g.id]: !isOpen(g) }))

  let body: React.ReactNode
  if (unavailable) {
    body = <p className="caps-empty" data-testid="caps-unavailable">{unavailable}</p>
  } else if (!payload) {
    body = (
      <p className="caps-empty" data-testid="caps-none">
        Nothing reported yet. An engine that reports what it loads (Claude Code does) sends it when a
        turn starts; this engine may not report capabilities at all.
      </p>
    )
  } else {
    const byId = new Map<string, CapabilityGroup>(payload.groups.map(g => [g.id, g]))
    const anyMatch = payload.groups.some(g => g.items.some(it => matchesQuery(it, q)))
    const sections = CAPABILITY_GROUPS.map(({ id, label }) => {
      const group = byId.get(id)
      if (!group) {
        if (q) return null
        return (
          <section key={id} className="caps-group not-reported" data-testid={`caps-group-${id}`}>
            <h3 className="caps-group-head static">
              <span className="caps-group-label">{label}</span>
              <span className="caps-group-missing">not reported by this engine</span>
            </h3>
          </section>
        )
      }
      const items = group.items.filter(it => matchesQuery(it, q))
      if (q && items.length === 0) return null
      const open = q !== '' || isOpen(group)
      const listId = `${titleId}-${id}`
      const count = q ? `${items.length} of ${group.items.length}` : String(group.items.length)
      return (
        <section key={id} className="caps-group" data-testid={`caps-group-${id}`}>
          <h3 className="caps-group-head">
            <button
              type="button"
              aria-expanded={open}
              aria-controls={listId}
              onClick={() => toggle(group)}
              disabled={q !== ''}
            >
              <span className="caps-chevron" aria-hidden="true">{open ? '▾' : '▸'}</span>
              <span className="caps-group-label">{group.label}</span>
              <span className="caps-count" data-testid={`caps-count-${id}`}>{count}</span>
            </button>
          </h3>
          {open && (
            <div id={listId} className="caps-group-body">
              {group.note && <p className="caps-group-note">{group.note}</p>}
              {group.items.length === 0 ? (
                <p className="caps-group-empty">None loaded.</p>
              ) : (
                <ul className="caps-items">
                  {items.map(it => <Item key={it.name} item={it} />)}
                </ul>
              )}
              {group.total !== undefined && group.total > group.items.length && (
                <p className="caps-group-note">Showing {group.items.length} of {group.total}.</p>
              )}
            </div>
          )}
        </section>
      )
    })
    body = (
      <>
        {sections}
        {q && !anyMatch && <p className="caps-empty" data-testid="caps-no-match">No matches for “{q}”.</p>}
      </>
    )
  }

  const meta: string[] = []
  if (report && payload) {
    const t = Date.parse(report.ts)
    if (Number.isFinite(t)) meta.push(`reported ${new Date(t).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })}`)
    if (payload.model) meta.push(payload.model)
    const engine = [payload.engine_name ?? payload.engine, payload.version].filter(Boolean).join(' ')
    if (engine) meta.push(engine)
  }

  return createPortal(
    <div
      className="caps-overlay"
      data-testid="caps-overlay"
      {...backdrop}
    >
      <div
        ref={dialogRef}
        className="caps-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        data-testid="capabilities-panel"
        onKeyDown={onKeyDown}
      >
        <header className="caps-header">
          <h2 id={titleId}>Skills &amp; plugins</h2>
          <button type="button" className="caps-close" aria-label="Close" onClick={onClose}>✕</button>
        </header>
        {payload && !unavailable && (
          <div className="caps-meta">
            {meta.length > 0 && <p data-testid="caps-meta">{meta.join(' · ')}</p>}
            {(payload.cwd || payload.permission_mode) && (
              <p className="caps-meta-dim">
                {[payload.cwd, payload.permission_mode && `permissions: ${payload.permission_mode}`].filter(Boolean).join(' · ')}
              </p>
            )}
            <p className="caps-meta-dim">
              {payload.note ?? 'What the session reported it loaded.'} It reflects what was loaded at start;
              changes on disk since then aren’t shown until the engine reports again.
            </p>
          </div>
        )}
        {payload && !unavailable && capabilityCount(payload) > 0 && (
          <input
            ref={searchRef}
            className="caps-search"
            type="search"
            placeholder="Filter by name, description, source…"
            aria-label="Filter skills, plugins and tools"
            value={query}
            onChange={e => setQuery(e.target.value)}
          />
        )}
        <div className="caps-body">{body}</div>
      </div>
    </div>,
    document.body,
  )
}

function Item({ item }: { item: CapabilityItem }) {
  const [expanded, setExpanded] = useState(false)
  const desc = item.description ?? ''
  const long = desc.length > DESC_PREVIEW
  return (
    <li className="caps-item" data-testid="caps-item">
      <div className="caps-item-line">
        <span className="caps-item-name">{item.name}</span>
        {item.status && <span className={`caps-chip status-${item.status}`}>{item.status}</span>}
        {item.source && <span className="caps-chip source">{item.source}</span>}
        {item.detail && <span className="caps-item-detail">{item.detail}</span>}
      </div>
      {desc && (
        <p className="caps-item-desc">
          {long && !expanded ? `${desc.slice(0, DESC_PREVIEW).trimEnd()}…` : desc}
          {long && (
            <button type="button" className="caps-more" aria-expanded={expanded} onClick={() => setExpanded(x => !x)}>
              {expanded ? 'less' : 'more'}
            </button>
          )}
        </p>
      )}
    </li>
  )
}
