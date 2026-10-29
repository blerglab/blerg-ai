// CapabilitiesPanel: what a session has loaded — skills, plugins, MCP
// servers, slash commands, tools, subagents — from its latest capabilities
// report. Driven only by the report's data (no per-engine branches): a group
// the report doesn't carry says "not reported by this engine". CapabilitiesView
// is the content, which the session panel's Capabilities tab shows; CapabilitiesPanel
// puts it in a dialog (a terminal session's, which says why it has none): a
// modal on desktop, a full-screen sheet on narrow screens (CSS). All text
// renders as plain text.
import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from 'react'
import { createPortal } from 'react-dom'
import type { CapabilityGroup, CapabilityItem } from '../types'
import PermissionChip from './PermissionChip'
import { CAPABILITY_GROUPS, asOfLabel, capabilityCount, humanModelName, matchesQuery, middleTruncate, permissionLabel, statusGlyph, statusWord, withLiveMcp, type CapabilitiesReport } from '../lib/capabilities'
import { builtinModels } from '../lib/engineModels'
import { ENGINE_LABELS, ENGINE_SHORT } from '../lib/runtimes'
import { RunGlyph } from './RunCard'
import type { CapabilitiesPayload } from '../types'
import { useBackdropClose, type McpLive } from '@blerglab/chat'
import './CapabilitiesPanel.css'
import InitialLoader from './InitialLoader'
import ToolSchemaGuidance from './ToolSchemaGuidance'
import { toolSchemaGuidanceText } from '../lib/toolSchemaGuidance'

// Groups longer than this start collapsed.
const COLLAPSE_OVER = 15

interface ViewProps {
  /** Only the first read without a report; known toolbar actions remain available. */
  initialLoading?: boolean
  report: CapabilitiesReport | null
  // When set, capabilities can't be reported for this session at all (a
  // terminal session); this says why, instead of the "nothing yet" state.
  unavailable?: string
  // Tool changes waiting on this session (MCP tools that changed or were
  // added); noted on the MCP servers group with a link to the Proposals page.
  pendingToolChanges?: number
  // The live state's MCP servers: their status now, over the report's start-up snapshot.
  liveMcp?: McpLive | null
  // The filter box, for a dialog that focuses it when it opens.
  searchRef?: RefObject<HTMLInputElement | null>
  // A control beside the filter on the toolbar row (the panel's "+ Add"), and what shows under the
  // row (the add form it opens, plugin requests).
  toolbarAction?: ReactNode
  belowToolbar?: ReactNode
  // An action under one item (an MCP server's sign-in), by group; null for none.
  itemAction?: (groupId: string, item: CapabilityItem) => ReactNode
}

interface Props extends Omit<ViewProps, 'searchRef'> {
  onClose: () => void
}

const FOCUSABLE = 'button:not([disabled]), input:not([disabled]), [href], [tabindex]:not([tabindex="-1"])'

export default function CapabilitiesPanel({ onClose, ...view }: Props) {
  const titleId = useId()
  const dialogRef = useRef<HTMLDivElement | null>(null)
  const backdrop = useBackdropClose(onClose)
  const searchRef = useRef<HTMLInputElement | null>(null)
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
        <CapabilitiesView {...view} searchRef={searchRef} />
      </div>
    </div>,
    document.body,
  )
}

/** What the session loaded, with the report's facts, a filter and a section per group. */
export function CapabilitiesView({ initialLoading = false, report, unavailable, pendingToolChanges, liveMcp, searchRef, toolbarAction, belowToolbar, itemAction }: ViewProps) {
  const listBase = useId()
  const [query, setQuery] = useState('')
  const snapshot = report?.payload ?? null
  const payload = useMemo(() => {
    if (snapshot) return withLiveMcp(snapshot, liveMcp)
    // Live connection state can arrive before the first loaded-tools report.
    // Keep other groups unreported rather than inventing a loaded snapshot.
    return liveMcp?.servers.length ? withLiveMcp({ engine: liveMcp.engine, groups: [] }, liveMcp) : null
  }, [snapshot, liveMcp])
  // Groups the user opened or closed; the rest follow their default (long
  // groups start closed), which also holds for a report that arrives later.
  const [openState, setOpenState] = useState<Record<string, boolean>>({})

  const q = query.trim()
  const isOpen = (g: CapabilityGroup) => openState[g.id] ?? g.items.length <= COLLAPSE_OVER
  const toggle = (g: CapabilityGroup) => setOpenState(prev => ({ ...prev, [g.id]: !isOpen(g) }))

  const pendingNote = pendingToolChanges && pendingToolChanges > 0 ? (
    <p className="caps-group-note" data-testid="caps-mcp-pending">
      {pendingToolChanges} tool {pendingToolChanges === 1 ? 'change' : 'changes'} pending: an MCP server changed or added a tool this session uses; it is off until you decide on the{' '}
      <a href="/proposals">Proposals page</a>.
    </p>
  ) : null

  let body: React.ReactNode
  if (unavailable) {
    body = <p className="caps-empty" data-testid="caps-unavailable">{unavailable}</p>
  } else if (!payload) {
    body = (
      <p className="caps-empty" data-testid="caps-none">
        Nothing reported yet. Engines that report what they load send a snapshot when the session
        starts or its tools become ready. Some engines do not expose this information.
      </p>
    )
  } else {
    const byId = new Map<string, CapabilityGroup>(payload.groups.map(g => [g.id, g]))
    const anyMatch = payload.groups.some(g => g.items.some(it => matchesQuery(it, q)))
    const sections = CAPABILITY_GROUPS.map(({ id, label }) => {
      const group = byId.get(id)
      if (!group) {
        const note = id === 'mcp' ? pendingNote : null
        if (q && !note) return null
        return (
          <section key={id} className="caps-group not-reported" data-testid={`caps-group-${id}`}>
            <h3 className="caps-group-head static">
              <span className="caps-group-label">{label}</span>
              <span className="caps-group-missing">not reported by this engine</span>
            </h3>
            {note}
          </section>
        )
      }
      const note = id === 'mcp' ? pendingNote : null
      const items = group.items.filter(it => matchesQuery(it, q))
      if (q && items.length === 0 && !note) return null
      // A pending note keeps its group open so it is never hidden.
      const open = q !== '' || isOpen(group) || note !== null
      const listId = `${listBase}-${id}`
      const count = q ? `${items.length} of ${group.items.length}` : String(group.items.length)
      return (
        <section key={id} className="caps-group" data-testid={`caps-group-${id}`}>
          <h3 className="caps-group-head">
            <button
              type="button"
              aria-expanded={open}
              aria-controls={listId}
              onClick={() => toggle(group)}
              disabled={q !== '' || note !== null}
            >
              <span className="caps-chevron" aria-hidden="true">{open ? '▾' : '▸'}</span>
              <span className="caps-group-label">{group.label}</span>
              <span className="caps-count" data-testid={`caps-count-${id}`}>{count}</span>
            </button>
          </h3>
          {open && (
            <div id={listId} className="caps-group-body">
              {note}
              {group.note && <p className="caps-group-note">{group.note}</p>}
              {group.items.length === 0 ? (
                <p className="caps-group-empty">None loaded.</p>
              ) : (
                <ul className="caps-items">
                  {items.map(it => <Item key={it.name} item={it} action={itemAction?.(id, it)} />)}
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

  const showSearch = !!payload && !unavailable && capabilityCount(payload) > 0
  return (
    <>
    {payload && !unavailable && <HeaderCard payload={payload} ts={report?.ts ?? ''} />}
    {(showSearch || toolbarAction) && (
      <div className="caps-toolbar" data-testid="caps-toolbar">
        {showSearch && (
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
        {toolbarAction}
      </div>
    )}
    {belowToolbar}
    <div className="caps-body"><InitialLoader loading={initialLoading} hasContent={!!payload || !!unavailable} label="capabilities" shape="facts">{body}</InitialLoader></div>
    </>
  )
}

/** The facts of the report in one compact card: engine and version with the model's name, then the
 *  workspace, the permission mode as a chip, and when it was reported, with an ⓘ that explains what
 *  the list is and when it changes. */
function HeaderCard({ payload, ts }: { payload: CapabilitiesPayload; ts: string }) {
  const infoId = useId()
  const [info, setInfo] = useState(false)
  const infoButton = useRef<HTMLButtonElement | null>(null)
  const card = useRef<HTMLElement | null>(null)
  // Closes on a press anywhere outside the card, as a popover does.
  useEffect(() => {
    if (!info) return
    const away = (e: PointerEvent) => { if (!card.current?.contains(e.target as Node)) setInfo(false) }
    document.addEventListener('pointerdown', away)
    return () => document.removeEventListener('pointerdown', away)
  }, [info])
  // Esc closes the open popover first, wherever the focus is (the filter, the page after a tap that
  // did not focus the button): caught on the way down, before the drawer or dialog closes on it.
  useEffect(() => {
    if (!info) return
    const esc = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      const el = card.current
      setInfo(false)
      // A card on a tab that is not on screen just closes; the Esc goes on to whatever wants it.
      if (!el || el.closest('[hidden]')) return
      e.stopPropagation()
      e.preventDefault()
      const at = document.activeElement
      if (!at || at === document.body || el.contains(at)) infoButton.current?.focus()
    }
    document.addEventListener('keydown', esc, true)
    return () => document.removeEventListener('keydown', esc, true)
  }, [info])

  const engineKey = payload.engine || 'claude'
  const engineName = payload.engine_name ?? ENGINE_LABELS[engineKey] ?? payload.engine
  const engineLine = [engineName, payload.version].filter(Boolean).join(' ')
  const model = payload.model ? humanModelName(payload.model, builtinModels(engineKey)) : ''
  const perm = payload.permission_mode ? permissionLabel(payload.permission_mode, payload.engine, payload.sandbox_mode) : null
  const asOf = asOfLabel(ts)
  const t = Date.parse(ts)
  const full = Number.isFinite(t) ? new Date(t).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }) : ''

  const about = `${payload.note ?? 'What the session reported it loaded.'} It reflects what was loaded at start; changes on disk since then aren’t shown until the engine reports again.`

  return (
    <section ref={card} className="rail-item caps-card" data-testid="caps-header" aria-label="What the session reported">
      <div className="caps-card-line">
        {engineKey && <RunGlyph label={`Engine: ${engineName || engineKey}`}>{ENGINE_SHORT[engineKey] ?? engineKey.charAt(0).toUpperCase()}</RunGlyph>}
        <span className="caps-card-title" data-testid="caps-meta">
          {engineLine}
          {engineLine && model && ' · '}
          {model && <span title={`Model: ${payload.model}`}>{model}</span>}
        </span>
      </div>
      <div className="caps-card-line caps-card-facts">
        {payload.cwd && <span className="caps-cwd" data-testid="caps-cwd" title={payload.cwd}>{middleTruncate(payload.cwd)}</span>}
        {perm && (
          <PermissionChip permission={perm} testId="caps-permission" />
        )}
        <span className="caps-asof">
          {asOf && <time dateTime={ts} title={full} data-testid="caps-asof">{asOf}</time>}
          <button ref={infoButton} type="button" className="caps-info-btn" aria-label="About this list" aria-expanded={info}
            aria-controls={info ? infoId : undefined} onClick={() => setInfo(v => !v)}>
            {/* ⓘ drawn in CSS: the character is missing from some system fonts. */}
            <span className="caps-info-i" aria-hidden="true">i</span>
          </button>
        </span>
      </div>
      {info && (
        <div id={infoId} className="caps-info" role="note" data-testid="caps-info">
          <p>{about}</p>
          <ToolSchemaGuidance policy={payload.tool_schema_refresh} className="caps-info-guidance" />
        </div>
      )}
      {/* Read out when the popover opens: a polite region that is always there, filled on open. */}
      <span className="rail-sr" role="status" aria-live="polite" data-testid="caps-info-announce">
        {info ? `${about} ${toolSchemaGuidanceText(payload.tool_schema_refresh)}` : ''}
      </span>
    </section>
  )
}

function Item({ item, action }: { item: CapabilityItem; action?: ReactNode }) {
  const [expanded, setExpanded] = useState(false)
  // A description is a button only when its one line is cut: measured, and again when the width changes.
  const [cut, setCut] = useState(false)
  const wrap = useRef<HTMLDivElement | null>(null)
  const desc = item.description ?? ''
  useLayoutEffect(() => {
    const el = wrap.current
    if (!el || expanded) return
    const measure = () => {
      const line = el.firstElementChild as HTMLElement | null
      if (line) setCut(line.scrollWidth > line.clientWidth + 1)
    }
    measure()
    if (typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [desc, expanded])
  const status = item.status ? statusGlyph(item.status) : null
  const word = item.status ? statusWord(item.status) : ''
  return (
    <li className="caps-item" data-testid="caps-item">
      <div className="caps-item-line">
        {status && (
          <span className={`caps-status status-${item.status} tone-${status.tone}`} title={word}>
            <span aria-hidden="true">{status.glyph}</span><span className="rail-sr">{word}</span>
          </span>
        )}
        <span className="caps-item-name" title={item.name}>{item.name}</span>
        {item.detail && <span className="caps-item-detail">{item.detail}</span>}
        {item.source && <span className="rail-chip caps-source" title={`Source: ${item.source}`}>{item.source}</span>}
      </div>
      {desc && (
        <div ref={wrap} className="caps-item-desc-wrap">
          {cut || expanded ? (
            <button type="button" className={`caps-item-desc${expanded ? ' open' : ''}`} aria-expanded={expanded} title={expanded ? undefined : desc}
              onClick={() => setExpanded(x => !x)}>
              {desc}
            </button>
          ) : (
            <span className="caps-item-desc">{desc}</span>
          )}
        </div>
      )}
      {action}
    </li>
  )
}
