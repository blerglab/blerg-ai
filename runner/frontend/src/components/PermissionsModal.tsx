// PermissionsModal: everything a session may do without asking, in one dialog with a search. Child
// sessions (per runtime, with their engines and every rule the server states), Boards (the person's
// boards, each with an access level: propose, read, file without asking, or full), MCP write tools
// (grouped by connection), and what is active now with how and when it was given. Nothing applies until Save; Cancel and Esc throw the
// draft away. Rows that Save would change wear an accent edge, and the footer counts them.
// From the session panel, Save goes through the grant APIs (owner-only on the server); from the
// launch sheet, Save hands the draft back and the server grants it as the session starts.
import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { GRANT_GLYPH } from '../lib/grantGlyphs'
import { createPortal } from 'react-dom'
import { useBackdropClose } from '@blerglab/chat'
import { ENGINE_LABELS } from '../lib/runtimes'
import {
  BOARD_LEVELS, BOARD_LEVEL_LABELS, BOARD_LEVEL_NOTE, boardLevelChip, boardLevelRank, changeCounts, changeSummary, diffPermissions, levelOf, matchesQuery,
  nextChildState, toolKey, type BoardLevel, type ChildOption, type ChildState, type PermissionChanges, type PermissionsDraft,
} from '../lib/permissionsModal'
import type { ChildRuntime } from '../hooks/useChildApproval'
import { useBoardOrigin } from '../lib/boardLink'
import './SessionPermissions.css'
import './PermissionsModal.css'

export interface ToolRow { connection_id: string; connection_name: string; tool: string; changed?: boolean }
/** Something given before the modal opened, as the Active section lists it. */
export interface ActiveEntry {
  key: string
  kind: 'child' | 'board' | 'tool'
  label: string
  /** How and when it was given. */
  detail: string
  /** Why it does not approve now (a dormant grant). */
  dormant?: string
  target: { board: string } | { tool: string } | { child: ChildRuntime } | { delegation: string } | { dormant: string }
}
export type SaveOutcome = void | { message: string; failed: PermissionChanges }
export type Loadable<T> = { state: 'loading' } | { state: 'failed'; message: string; retry?: () => void } | { state: 'ready'; value: T } | { state: 'none' }

export interface PermissionsModalProps {
  mode: 'session' | 'launch'
  onClose: () => void
  /** Where the focus starts: the search, or the first child toggle (from a chat card's link). */
  initialFocus?: 'search' | 'children'
  childSessions: Loadable<{ options: ChildOption[]; note: string }>
  boards: Loadable<{ id: string; name: string }[]>
  tools: ToolRow[]
  /** Proposal grants (boards and tools) may be given; otherwise `grantReason` says why not. */
  canGrant: boolean
  grantReason?: string
  /** The draft as the modal opened (children may arrive later; they are read from `children`). */
  initial: Omit<PermissionsDraft, 'children'>
  active: ActiveEntry[]
  dormantScopes?: Record<string, { board?: string; tool?: string }>
  /** Per board: chosen at launch (its engine also has the board's own tools), or why its level does not act now. */
  boardInfo?: Record<string, { launch?: boolean; dormant?: string }>
  /** A session started by another one: who started it and that session's level per board now (a board
   *  not listed is at propose). No level above it can be chosen; the session acts at the lower one. */
  boardCap?: { by: string; levels: Record<string, BoardLevel> }
  /** Applies the changes; resolves to nothing when all of it applied, or to what to say and which
   *  changes did not apply (kept, ticked and marked, for another Save). */
  onSave: (next: PermissionsDraft, changes: PermissionChanges) => Promise<SaveOutcome>
}

const KIND_GLYPH = GRANT_GLYPH
const FOCUSABLE = 'button:not([disabled]), input:not([disabled]):not([tabindex="-1"]), [href], [tabindex]:not([tabindex="-1"])'
const engineNames = (ids: string[]) => ids.map(id => ENGINE_LABELS[id] ?? id).join(', ')

interface Edits {
  children: Partial<Record<ChildRuntime, ChildState>>
  /** Boards whose level the person changed (overrides on the initial levels). */
  boardLevels: Record<string, BoardLevel>
  /** Signed-out boards to renew at their level. */
  renewBoards: string[]
  toolsOn: string[]; toolsOff: string[]
  revokeDelegations: string[]; revokeDormant: string[]
}
const NO_EDITS: Edits = { children: {}, boardLevels: {}, renewBoards: [], toolsOn: [], toolsOff: [], revokeDelegations: [], revokeDormant: [] }
const flip = (list: string[], id: string) => list.includes(id) ? list.filter(x => x !== id) : [...list, id]
const without = (list: string[], id: string) => list.filter(x => x !== id)

export default function PermissionsModal(props: PermissionsModalProps) {
  const { mode, onClose, initialFocus = 'search', childSessions: children, boards, tools, canGrant, grantReason, initial, active, dormantScopes = {}, boardInfo = {}, boardCap, onSave } = props
  const boardBase = useBoardOrigin()
  const [boardWindow, setBoardWindow] = useState({ query: '', page: 0 })
  const [saveResults, setSaveResults] = useState<{ saved: { key: string; label: string }[]; failed: { key: string; label: string }[] } | null>(null)
  const titleId = useId()
  const searchId = useId()
  const dialogRef = useRef<HTMLDivElement>(null)
  const searchRef = useRef<HTMLInputElement>(null)
  const [saving, setSaving] = useState(false)
  const [query, setQuery] = useState('')
  const [edits, setEdits] = useState<Edits>(NO_EDITS)
  const [error, setError] = useState('')
  // Keys of changes the last Save could not apply ("b:<board>", "t:<tool>", "c:<runtime>", ...).
  const [failedKeys, setFailedKeys] = useState<string[]>([])
  // The edits a Save left behind. Any later edit makes its results stale, so they are cleared.
  const savedEdits = useRef<Edits | null>(null)
  useEffect(() => {
    if (savedEdits.current && edits !== savedEdits.current) { savedEdits.current = null; setSaveResults(null) }
  }, [edits])
  const [collapsed, setCollapsed] = useState<string[]>([])
  const [maximized, setMaximized] = useState(false)

  const childOptions = useMemo(() => children.state === 'ready' ? children.value.options : [], [children])
  const initialDraft: PermissionsDraft = useMemo(() => {
    const c: PermissionsDraft['children'] = {}
    for (const o of childOptions) c[o.runtime] = o.state
    return { ...initial, children: c }
  }, [initial, childOptions])
  const draft: PermissionsDraft = useMemo(() => ({
    children: { ...initialDraft.children, ...edits.children },
    boards: Object.fromEntries(Object.entries({ ...initialDraft.boards, ...edits.boardLevels }).filter(([, level]) => level !== 'propose')),
    // A renewal stands only while the board keeps the level it had; a new level renews it anyway.
    renewBoards: edits.renewBoards.filter(b => !(b in edits.boardLevels)),
    tools: [...initialDraft.tools.filter(t => !edits.toolsOff.includes(t)), ...edits.toolsOn.filter(t => !initialDraft.tools.includes(t))],
    revokeDelegations: [...initialDraft.revokeDelegations, ...edits.revokeDelegations],
    revokeDormant: [...initialDraft.revokeDormant, ...edits.revokeDormant],
  }), [initialDraft, edits])
  const changes = useMemo(() => diffPermissions(initialDraft, draft, dormantScopes), [initialDraft, draft, dormantScopes])
  const counts = changeCounts(changes)
  const dirty = counts.grant + counts.revoke > 0
  // Neither a save in flight nor unsaved changes are thrown away by a stray click beside the dialog.
  const backdrop = useBackdropClose(() => { if (!saving && !dirty) onClose() })

  const setBoard = (id: string, level: BoardLevel) => setEdits(e => {
    const rest = { ...e.boardLevels }
    if (level === levelOf(initialDraft, id)) delete rest[id]
    else rest[id] = level
    return { ...e, boardLevels: rest }
  })
  const setTool = (key: string, on: boolean) => setEdits(e => {
    const was = initialDraft.tools.includes(key)
    return { ...e, toolsOn: on && !was ? [...without(e.toolsOn, key), key] : without(e.toolsOn, key), toolsOff: !on && was ? [...without(e.toolsOff, key), key] : without(e.toolsOff, key) }
  })
  const setChild = (o: ChildOption) => setEdits(e => {
    const current = draft.children[o.runtime] ?? 'off'
    const next = nextChildState(o, current)
    const rest = { ...e.children }
    if (next === initialDraft.children[o.runtime]) delete rest[o.runtime]
    else rest[o.runtime] = next
    return { ...e, children: rest }
  })

  // Focus: into the dialog on open, back to the opener on close.
  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null
    if (initialFocus === 'search') searchRef.current?.focus()
    else dialogRef.current?.focus()
    return () => { if (opener?.isConnected) opener.focus?.() }
  }, [initialFocus])
  // From a chat card: the first child toggle, once the children have loaded, if the person has not moved.
  const focusedChildren = useRef(false)
  useEffect(() => {
    if (initialFocus !== 'children' || focusedChildren.current || children.state === 'loading') return
    focusedChildren.current = true
    if (document.activeElement !== dialogRef.current) return
    const box = dialogRef.current?.querySelector<HTMLInputElement>('[data-section="children"] input:not([disabled])')
    if (box) { box.scrollIntoView?.({ block: 'nearest' }); box.focus() }
  }, [initialFocus, children.state])
  // Lists are one tab stop each; arrows, Home and End move within them (roving tabindex).
  useLayoutEffect(() => {
    for (const list of dialogRef.current?.querySelectorAll<HTMLElement>('[data-roving]') ?? []) {
      const items = [...list.querySelectorAll<HTMLInputElement>('input[type="checkbox"]:not(:disabled)')]
      const current = items.find(i => i === document.activeElement) ?? items.find(i => i.dataset.current === 'true') ?? items[0]
      for (const i of items) i.tabIndex = i === current ? 0 : -1
    }
  })
  function onListKey(e: React.KeyboardEvent<HTMLElement>) {
    const items = [...e.currentTarget.querySelectorAll<HTMLInputElement>('input[type="checkbox"]:not(:disabled)')]
    const at = items.indexOf(document.activeElement as HTMLInputElement)
    let to = -1
    if (e.key === 'ArrowDown') to = Math.min(items.length - 1, at + 1)
    else if (e.key === 'ArrowUp') to = Math.max(0, at - 1)
    else if (e.key === 'Home') to = 0
    else if (e.key === 'End') to = items.length - 1
    if (to < 0 || !items[to]) return
    e.preventDefault()
    for (const i of items) { i.tabIndex = -1; delete i.dataset.current }
    items[to].tabIndex = 0; items[to].dataset.current = 'true'; items[to].focus()
  }
  function onKeyDown(e: React.KeyboardEvent) {
    // Drawn in a portal, but React bubbles its keys to the launch sheet or the panel's drawer, which
    // close on Escape and trap Tab for themselves: keys typed here are the dialog's alone.
    e.stopPropagation()
    if (e.key === 'Escape') {
      e.preventDefault()
      // A search in progress is cleared first; the next Esc closes.
      if (query && document.activeElement === searchRef.current) { setQuery(''); return }
      if (!saving) onClose()
      return
    }
    if (e.key !== 'Tab' || !dialogRef.current) return
    const nodes = [...dialogRef.current.querySelectorAll<HTMLElement>(FOCUSABLE)].filter(n => n.offsetParent !== null || n === document.activeElement)
    if (!nodes.length) return
    const first = nodes[0]; const last = nodes[nodes.length - 1]
    if (e.shiftKey && (document.activeElement === first || document.activeElement === dialogRef.current)) { e.preventDefault(); last.focus() }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus() }
  }

  async function save() {
    if (!dirty || saving) return
    setSaving(true); setError(''); setFailedKeys([])
    try {
      const outcome = await onSave(draft, changes)
      if (!outcome) { onClose(); return }
      // What applied is now the server's; what did not stays as a change, ticked and marked.
      const f = outcome.failed
      const children: Edits['children'] = {}
      for (const c of f.children) children[c.runtime] = c.enabled ? 'on' : 'off'
      const remaining: Edits = {
        children,
        boardLevels: Object.fromEntries(f.boardLevels.filter(b => b.from !== b.level).map(b => [b.board, b.level])),
        renewBoards: f.boardLevels.filter(b => b.from === b.level).map(b => b.board),
        toolsOn: f.grantTools.map(t => toolKey(t.connection_id, t.tool)), toolsOff: f.revokeTools.map(t => toolKey(t.connection_id, t.tool)),
        revokeDelegations: f.revokeDelegations, revokeDormant: f.revokeDormant,
      }
      savedEdits.current = remaining
      setEdits(remaining)
      const keys = [
        ...f.children.map(c => `c:${c.runtime}`), ...f.boardLevels.map(b => `b:${b.board}`),
        ...[...f.grantTools, ...f.revokeTools].map(t => `t:${toolKey(t.connection_id, t.tool)}`),
        ...f.revokeDelegations.map(k => `d:${k}`), ...f.revokeDormant.map(id => `x:${id}`),
      ]
      setFailedKeys(keys)
      const attempted = changedRows(changes)
      setSaveResults({ saved: attempted.filter(row => !keys.includes(row.key)), failed: attempted.filter(row => keys.includes(row.key)) })
      setError(outcome.message)
    } catch (e) { setError(e instanceof Error ? e.message : 'Could not save the permissions.') }
    finally { setSaving(false) }
  }
  const notSaved = (key: string) => failedKeys.includes(key) ? <span className="pm-row-note pm-failed-note">Not saved</span> : saveResults?.saved.some(row => row.key === key) ? <span className="pm-row-note">Saved</span> : null

  // ── What the search leaves ──
  const shownChildren = childOptions.filter(o => matchesQuery(query, o.label, engineNames(o.engines), 'children child'))
  const boardList = boards.state === 'ready' ? boards.value : []
  const shownBoards = boardList.filter(b => matchesQuery(query, b.name))
  const boardPage = Math.min(boardWindow.query === query ? boardWindow.page : 0, Math.max(0, Math.ceil(shownBoards.length / 25) - 1))
  const visibleBoards = shownBoards.slice(boardPage * 25, (boardPage + 1) * 25)
  const connections = useMemo(() => {
    const m = new Map<string, { id: string; name: string; tools: ToolRow[] }>()
    for (const t of tools) {
      const g = m.get(t.connection_id) ?? { id: t.connection_id, name: t.connection_name, tools: [] }
      g.tools.push(t); m.set(t.connection_id, g)
    }
    return [...m.values()]
  }, [tools])
  const shownConnections = connections.map(c => ({ ...c, tools: matchesQuery(query, c.name) ? c.tools : c.tools.filter(t => matchesQuery(query, c.name, t.tool)) })).filter(c => c.tools.length > 0)

  // ── Active: given before, plus what Save would add ──
  const boardName = (id: string) => boardList.find(b => b.id === id)?.name ?? active.find(a => 'board' in a.target && a.target.board === id)?.label ?? id
  const toolName = (key: string) => { const t = tools.find(x => toolKey(x.connection_id, x.tool) === key); return t ? `${t.connection_name} · ${t.tool}` : key }
  function changedRows(c: PermissionChanges) {
    return [
      ...c.children.map(x => ({ key: `c:${x.runtime}`, label: childOptions.find(o => o.runtime === x.runtime)?.label ?? x.runtime })),
      ...c.boardLevels.map(x => ({ key: `b:${x.board}`, label: boardName(x.board) })),
      ...[...c.grantTools, ...c.revokeTools].map(x => ({ key: `t:${toolKey(x.connection_id, x.tool)}`, label: toolName(toolKey(x.connection_id, x.tool)) })),
      ...c.revokeDelegations.map(key => ({ key: `d:${key}`, label: active.find(a => 'delegation' in a.target && a.target.delegation === key)?.label ?? key })),
      ...c.revokeDormant.map(id => ({ key: `x:${id}`, label: active.find(a => a.key === id)?.label ?? id })),
    ]
  }
  const revoking = (a: ActiveEntry) => 'board' in a.target ? levelOf(draft, a.target.board) !== levelOf(initialDraft, a.target.board)
    : 'tool' in a.target ? !draft.tools.includes(a.target.tool)
      : 'child' in a.target ? draft.children[a.target.child] !== 'on'
      : 'delegation' in a.target ? draft.revokeDelegations.includes(a.target.delegation) || changes.children.some(c => !c.enabled && 'delegation' in a.target && a.target.delegation.startsWith(`${c.runtime}\u0000`))
        : draft.revokeDormant.includes(a.target.dormant)
  const toggleRevoke = (a: ActiveEntry) => {
    if ('board' in a.target) setBoard(a.target.board, revoking(a) ? levelOf(initialDraft, a.target.board) : 'propose')
    else if ('tool' in a.target) setTool(a.target.tool, revoking(a))
    else if ('child' in a.target) { const r = a.target.child; const o = childOptions.find(x => x.runtime === r); if (o) setChild(o) }
    else if ('delegation' in a.target) { const k = a.target.delegation; setEdits(e => ({ ...e, revokeDelegations: flip(e.revokeDelegations, k) })) }
    else { const k = a.target.dormant; setEdits(e => ({ ...e, revokeDormant: flip(e.revokeDormant, k) })) }
  }
  const additions: { key: string; kind: ActiveEntry['kind']; label: string; undo: () => void }[] = [
    ...childOptions.filter(o => draft.children[o.runtime] === 'on' && initialDraft.children[o.runtime] !== 'on').map(o => ({ key: `c:${o.runtime}`, kind: 'child' as const, label: o.label, undo: () => setChild(o) })),
    ...Object.entries(draft.boards).filter(([b]) => levelOf(initialDraft, b) === 'propose').map(([b, level]) => ({ key: `b:${b}`, kind: 'board' as const, label: boardLevelChip(boardName(b), level), undo: () => setBoard(b, 'propose') })),
    ...draft.tools.filter(t => !initialDraft.tools.includes(t)).map(t => ({ key: `t:${t}`, kind: 'tool' as const, label: `Run ${toolName(t)} without asking`, undo: () => setTool(t, false) })),
  ]
  const shownActive = active.filter(a => matchesQuery(query, a.label, a.detail))
  const shownAdditions = additions.filter(a => matchesQuery(query, a.label))
  const pendingNote = mode === 'launch' ? 'Given as the session starts' : 'Applies on Save'

  const boardsWithAccess = boardList.filter(b => levelOf(draft, b.id) !== 'propose').length
  // An active board row says what Save does to it: revoked (back to propose) or its new level.
  const boardOutcome = (a: ActiveEntry) => 'board' in a.target
    ? (levelOf(draft, a.target.board) === 'propose' ? 'Revoked on Save' : `Becomes ${BOARD_LEVEL_LABELS[levelOf(draft, a.target.board)]} on Save`)
    : 'Revoked on Save'
  const nothing = (what: string) => <p className="pm-empty">{query ? `No ${what} match “${query.trim()}”.` : `No ${what}.`}</p>

  return createPortal(
    <div className="pm-overlay" data-testid="permissions-overlay" {...backdrop}>
      <div ref={dialogRef} className={`pm-dialog${maximized ? ' pm-maximized' : ''}`} role="dialog" aria-modal="true" aria-labelledby={titleId} tabIndex={-1}
        data-testid="permissions-modal" onKeyDown={onKeyDown}>
        <header className="pm-head">
          <div className="pm-title">
            <h2 id={titleId}>Permissions</h2>
            <p>{mode === 'launch' ? 'What the new session may do without asking you. Given as it starts.' : 'What this session may do without asking you. Each one still runs every check of your approval, as you.'}</p>
          </div>
          <button type="button" className="pm-close pm-maximize" aria-label={maximized ? 'Restore size' : 'Maximize'} aria-pressed={maximized}
            onClick={() => setMaximized(m => !m)}>{maximized ? '⤡' : '⤢'}</button>
          <button type="button" className="pm-close" aria-label="Close" onClick={onClose} disabled={saving}>✕</button>
        </header>
        <div className="pm-search">
          <label htmlFor={searchId} className="pm-visually-hidden">Search permissions</label>
          <input ref={searchRef} id={searchId} type="search" placeholder="Search boards, tools, engines…" value={query} onChange={e => setQuery(e.target.value)} autoComplete="off" />
        </div>
        <div className="pm-body">
          {/* ── Child sessions ── */}
          <section className="pm-section" data-section="children" aria-labelledby={`${titleId}-children`}>
            <h3 id={`${titleId}-children`}><span className="pm-glyph" aria-hidden="true">{KIND_GLYPH.child}</span>Child sessions</h3>
            {children.state === 'loading' && <p className="pm-empty" role="status">Loading child approvals…</p>}
            {children.state === 'none' && <p className="pm-empty">This session cannot start child sessions.</p>}
            {children.state === 'failed' && <p className="pm-empty pm-error" role="alert">{children.message} {children.retry && <button type="button" className="perm-revoke" onClick={children.retry}>Retry</button>}</p>}
            {children.state === 'ready' && (shownChildren.length === 0 ? nothing('child options') : <>
              <ul className="pm-list" data-roving onKeyDown={onListKey} aria-label="Child sessions">
                {shownChildren.map(o => <ChildRow key={o.runtime} failed={failedKeys.includes(`c:${o.runtime}`)} option={o} state={draft.children[o.runtime] ?? 'off'} changed={o.runtime in edits.children} disabled={saving} onToggle={() => setChild(o)} />)}
              </ul>
              {children.value.note && <p className="pm-note">{children.value.note}</p>}
            </>)}
          </section>

          {/* ── Boards ── */}
          <section className="pm-section" data-section="boards" aria-labelledby={`${titleId}-boards`}>
            <div className="pm-section-head">
              <h3 id={`${titleId}-boards`}><span className="pm-glyph" aria-hidden="true">{KIND_GLYPH.board}</span>Boards</h3>
              {canGrant && boards.state === 'ready' && boardList.length > 0 && <span className="pm-count" data-testid="pm-board-count">{boardsWithAccess} of {boardList.length} with access</span>}
            </div>
            {!canGrant ? <p className="pm-empty" data-testid="pm-grant-reason">{grantReason || 'This session cannot be given these permissions.'}</p>
              : boards.state === 'loading' ? <p className="pm-empty" role="status">Checking your boards…</p>
                : boards.state === 'failed' ? <p className="pm-empty pm-error" role="alert">{boards.message} {boards.retry && <button type="button" className="perm-revoke" onClick={boards.retry}>Retry</button>}</p>
                  : boards.state === 'none' || boardList.length === 0 ? <p className="pm-empty">No boards.</p>
                    : <>
                      <p className="pm-note pm-levels-note">{BOARD_LEVEL_NOTE}</p>
                      {boardCap && <p className="pm-note pm-cap-note" data-testid="pm-board-cap-note">Started by {boardCap.by}: this session never has more access to a board than that session has now, and acts at the lower of the two.</p>}
                      {shownBoards.length === 0 ? nothing('boards')
                        : <ul className="pm-list pm-scroll" aria-label="Board access">
                          {visibleBoards.map(b => {
                            const level = levelOf(draft, b.id)
                            const info = boardInfo[b.id]
                            const renewing = (draft.renewBoards ?? []).includes(b.id)
                            const canRenew = !!info?.dormant && level !== 'propose' && level === levelOf(initialDraft, b.id)
                            const cap = boardCap ? boardCap.levels[b.id] ?? 'propose' : undefined
                            return <li key={b.id} className={`pm-row pm-board-row${level !== levelOf(initialDraft, b.id) || renewing ? ' pm-changed' : ''}`} data-testid={`pm-board-${b.id}`}>
                              <span className="pm-board-name"><a className="pm-link" href={`${boardBase}/boards/${encodeURIComponent(b.id)}`} target="_blank" rel="noopener">{b.name}</a>{notSaved(`b:${b.id}`)}
                                {info?.launch && <span className="pm-row-note">Chosen at launch: its engine also has the board's own tools.</span>}
                                {info?.dormant && <span className="pm-row-note pm-warn">{renewing ? 'Renewed from your current sign-in on Save' : info.dormant}</span>}
                                {cap && (level !== 'propose' || cap !== 'propose') && <span className={`pm-row-note${boardLevelRank(level) > boardLevelRank(cap) ? ' pm-warn' : ''}`} data-testid={`pm-board-cap-${b.id}`}>
                                  {boardLevelRank(level) > boardLevelRank(cap) ? `Acts at ${BOARD_LEVEL_LABELS[cap]}: capped by ${boardCap?.by}, which has ${BOARD_LEVEL_LABELS[cap]}.` : `At most ${BOARD_LEVEL_LABELS[cap]}: what ${boardCap?.by} has.`}</span>}</span>
                              {canRenew && <button type="button" className="perm-revoke" disabled={saving} aria-pressed={renewing} aria-label={renewing ? `Keep ${b.name} as it is` : `Renew access to ${b.name}`}
                                onClick={() => setEdits(e => ({ ...e, renewBoards: renewing ? e.renewBoards.filter(x => x !== b.id) : [...e.renewBoards, b.id] }))}>{renewing ? 'Undo' : 'Renew'}</button>}
                              <select className="pm-level" value={level} disabled={saving} aria-label={`Access to ${b.name}`} onChange={e => setBoard(b.id, e.target.value as BoardLevel)}>
                                {BOARD_LEVELS.map(l => {
                                  // Above the cap of a started session: never offered (the server refuses it too).
                                  const above = !!cap && boardLevelRank(l) > boardLevelRank(cap)
                                  return <option key={l} value={l} disabled={above && l !== levelOf(initialDraft, b.id)}>{BOARD_LEVEL_LABELS[l]}{above ? ' (above the cap)' : ''}</option>
                                })}
                              </select>
                            </li>
                          })}
                        </ul>}
                      {shownBoards.length > 25 && <nav className="pm-board-pages" aria-label="Board pages">
                        <button className="pm-button" type="button" disabled={boardPage === 0} onClick={() => setBoardWindow({ query, page: boardPage - 1 })} aria-label="Previous boards">Previous</button>
                        <span role="status">Boards {boardPage * 25 + 1}–{Math.min(shownBoards.length, (boardPage + 1) * 25)} of {shownBoards.length}</span>
                        <button className="pm-button" type="button" disabled={(boardPage + 1) * 25 >= shownBoards.length} onClick={() => setBoardWindow({ query, page: boardPage + 1 })} aria-label="Next boards">Next</button>
                      </nav>}
                    </>}
          </section>

          {/* ── MCP write tools ── */}
          <section className="pm-section" data-section="tools" aria-labelledby={`${titleId}-tools`}>
            <h3 id={`${titleId}-tools`}><span className="pm-glyph" aria-hidden="true">{KIND_GLYPH.tool}</span>MCP write tools</h3>
            {!canGrant ? <p className="pm-empty">{grantReason || 'This session cannot be given these permissions.'}</p>
              : connections.length === 0 ? <p className="pm-empty">{mode === 'launch' ? 'Tools you set to "queued for your approval" can be allowed here.' : 'No tools of this session wait for your approval.'}</p>
                : shownConnections.length === 0 ? nothing('tools')
                  : shownConnections.map(c => {
                    const open = !!query.trim() || !collapsed.includes(c.id)
                    const allowed = c.tools.filter(t => draft.tools.includes(toolKey(t.connection_id, t.tool))).length
                    const listId = `${titleId}-c-${c.id}`
                    return <div key={c.id} className="pm-connection">
                      <button type="button" className="pm-connection-head" aria-expanded={open} aria-controls={open ? listId : undefined} onClick={() => setCollapsed(l => flip(l, c.id))}>
                        <span aria-hidden="true" className="pm-caret">{open ? '▾' : '▸'}</span><span className="pm-connection-name">{c.name}</span>
                        <span className="pm-count">{allowed} of {c.tools.length} allowed</span>
                      </button>
                      {open && <ul id={listId} className="pm-list" data-roving onKeyDown={onListKey} aria-label={`${c.name} tools`}>
                        {c.tools.map(t => {
                          const key = toolKey(t.connection_id, t.tool)
                          const on = draft.tools.includes(key)
                          return <li key={key} className={`pm-row${on !== initialDraft.tools.includes(key) ? ' pm-changed' : ''}`}>
                            <label className="pm-check"><input type="checkbox" checked={on} disabled={saving} onChange={e => setTool(key, e.target.checked)} aria-label={`Run ${c.name} · ${t.tool} without asking`} />
                              <span><span className="pm-tool">{t.tool}</span>{notSaved(`t:${key}`)}
                                {t.changed && <span className="pm-row-note pm-warn">Its definition changed since it was allowed. Allow it again to approve the new version.</span>}</span></label>
                          </li>
                        })}
                      </ul>}
                    </div>
                  })}
          </section>

          {/* ── Active ── */}
          <section className="pm-section" data-section="active" aria-labelledby={`${titleId}-active`}>
            <h3 id={`${titleId}-active`}>{mode === 'launch' ? 'Selected' : 'Active'} <span className="pm-count">{active.length}</span></h3>
            {shownActive.length + shownAdditions.length === 0 ? (query ? nothing('permissions') : <p className="pm-empty">{mode === 'launch' ? 'Nothing selected: the session will ask each time.' : 'Nothing yet: the session asks you each time.'}</p>)
              : <ul className="pm-list pm-active">
                {shownActive.map(a => {
                  const out = revoking(a)
                  return <li key={a.key} className={`pm-active-row${out ? ' pm-changed pm-revoking' : ''}${a.dormant ? ' pm-dormant' : ''}`} data-testid="pm-active-row">
                    <span className="pm-glyph" aria-hidden="true">{KIND_GLYPH[a.kind]}</span>
                    <span className="pm-active-text"><span className="pm-active-label">{a.label}</span>
                      {notSaved('delegation' in a.target ? `d:${a.target.delegation}` : 'dormant' in a.target ? `x:${a.target.dormant}` : '')}<span className="pm-row-note">{out ? boardOutcome(a) : a.detail}{a.dormant && !out && <span className="pm-warn"> · {a.dormant}</span>}</span></span>
                    <button type="button" className="perm-revoke" disabled={saving} aria-label={`${out ? 'Keep' : 'Revoke'}: ${a.label}`} onClick={() => toggleRevoke(a)}>{out ? 'Undo' : 'Revoke'}</button>
                  </li>
                })}
                {shownAdditions.map(a => <li key={a.key} className="pm-active-row pm-changed" data-testid="pm-active-row">
                  <span className="pm-glyph" aria-hidden="true">{KIND_GLYPH[a.kind]}</span>
                  <span className="pm-active-text"><span className="pm-active-label">{a.label}</span><span className="pm-row-note">{pendingNote}</span></span>
                  <button type="button" className="perm-revoke" disabled={saving} aria-label={`Remove: ${a.label}`} onClick={a.undo}>Remove</button>
                </li>)}
              </ul>}
          </section>
        </div>
        {saveResults && <section className="pm-save-results" role="region" aria-label="Save results">
          {saveResults.saved.length > 0 && <p>Saved: {saveResults.saved.map(row => row.label).join(', ')}</p>}
          {saveResults.failed.length > 0 && <p>Not saved: {saveResults.failed.map(row => row.label).join(', ')}</p>}
          {dirty && changedRows(changes).every(row => failedKeys.includes(row.key)) && <button className="pm-button" type="button" disabled={saving} onClick={() => void save()}>Retry failed changes</button>}
        </section>}
        {error && <p className="pm-error pm-save-error" role="alert">{error}</p>}
        <footer className="pm-foot">
          <span className={`pm-summary${dirty ? ' pm-summary-dirty' : ''}`} data-testid="pm-summary" aria-live="polite">{changeSummary(changes)}</span>
          <button type="button" className="pm-button" onClick={onClose} disabled={saving}>Cancel</button>
          <button type="button" className="pm-button pm-primary" onClick={() => void save()} disabled={!dirty || saving}>{saving ? 'Saving…' : 'Save'}</button>
        </footer>
      </div>
    </div>,
    document.body,
  )
}

function ChildRow({ option: o, state, changed, failed, disabled, onToggle }: { option: ChildOption; state: ChildState; changed: boolean; failed: boolean; disabled: boolean; onToggle: () => void }) {
  const input = useRef<HTMLInputElement>(null)
  const noteId = useId()
  useEffect(() => { if (input.current) input.current.indeterminate = state === 'partial' }, [state])
  return <li className={`pm-row${changed ? ' pm-changed' : ''}${o.disabled ? ' pm-row-disabled' : ''}`} data-testid={`pm-children-${o.runtime}`}>
    <label className="pm-check"><input ref={input} type="checkbox" checked={state === 'on'} disabled={disabled || o.disabled} onChange={onToggle} aria-describedby={noteId} />
      <span>{o.label}</span></label>
    <div id={noteId} className="pm-row-notes">
      {failed && <span className="pm-row-note pm-failed-note">Not saved</span>}
      {o.note && <span className="pm-row-note">{o.note}</span>}
      {o.engines.length > 0 && <span className="pm-row-note">Engines: {engineNames(o.engines)}</span>}
      {state === 'partial' && <span className="pm-row-note">Allowed for {engineNames(o.grantedEngines)} only. {o.canEnable ? 'Tick to allow every engine.' : 'Untick to revoke.'}</span>}
      {o.reason && <span className="pm-row-note pm-reason">{o.reason}</span>}
      {o.standingEngines.length > 0 && <span className="pm-row-note">{o.runtime === 'cluster' ? 'Cluster' : 'Desktop'} rules still allow {engineNames(o.standingEngines)} independently. Manage those in Settings → Sessions starting sessions.</span>}
    </div>
  </li>
}
