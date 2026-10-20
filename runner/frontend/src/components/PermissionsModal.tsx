// PermissionsModal: everything a session may do without asking, in one dialog with a search. Child
// sessions (per runtime, with their engines and every rule the server states), board filing (a
// checkable list of the person's boards), MCP write tools (grouped by connection), and what is
// active now with how and when it was given. Nothing applies until Save; Cancel and Esc throw the
// draft away. Rows that Save would change wear an accent edge, and the footer counts them.
// From the session panel, Save goes through the grant APIs (owner-only on the server); from the
// launch sheet, Save hands the draft back and the server grants it as the session starts.
import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useBackdropClose } from '@blerglab/chat'
import { ENGINE_LABELS } from '../lib/runtimes'
import {
  changeCounts, changeSummary, diffPermissions, matchesQuery, nextChildState, toggleShown, toolKey,
  type ChildOption, type ChildState, type PermissionChanges, type PermissionsDraft,
} from '../lib/permissionsModal'
import type { ChildRuntime } from '../hooks/useChildApproval'
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
  /** Applies the changes; resolves to nothing when all of it applied, or to what to say and which
   *  changes did not apply (kept, ticked and marked, for another Save). */
  onSave: (next: PermissionsDraft, changes: PermissionChanges) => Promise<SaveOutcome>
}

const KIND_GLYPH = { child: '↳', board: '▦', tool: '⚙' } as const
const FOCUSABLE = 'button:not([disabled]), input:not([disabled]):not([tabindex="-1"]), [href], [tabindex]:not([tabindex="-1"])'
const engineNames = (ids: string[]) => ids.map(id => ENGINE_LABELS[id] ?? id).join(', ')

interface Edits {
  children: Partial<Record<ChildRuntime, ChildState>>
  boardsOn: string[]; boardsOff: string[]
  toolsOn: string[]; toolsOff: string[]
  revokeDelegations: string[]; revokeDormant: string[]
}
const NO_EDITS: Edits = { children: {}, boardsOn: [], boardsOff: [], toolsOn: [], toolsOff: [], revokeDelegations: [], revokeDormant: [] }
const flip = (list: string[], id: string) => list.includes(id) ? list.filter(x => x !== id) : [...list, id]
const without = (list: string[], id: string) => list.filter(x => x !== id)

export default function PermissionsModal(props: PermissionsModalProps) {
  const { mode, onClose, initialFocus = 'search', childSessions: children, boards, tools, canGrant, grantReason, initial, active, dormantScopes = {}, onSave } = props
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
  const [collapsed, setCollapsed] = useState<string[]>([])

  const childOptions = useMemo(() => children.state === 'ready' ? children.value.options : [], [children])
  const initialDraft: PermissionsDraft = useMemo(() => {
    const c: PermissionsDraft['children'] = {}
    for (const o of childOptions) c[o.runtime] = o.state
    return { ...initial, children: c }
  }, [initial, childOptions])
  const draft: PermissionsDraft = useMemo(() => ({
    children: { ...initialDraft.children, ...edits.children },
    boards: [...initialDraft.boards.filter(b => !edits.boardsOff.includes(b)), ...edits.boardsOn.filter(b => !initialDraft.boards.includes(b))],
    tools: [...initialDraft.tools.filter(t => !edits.toolsOff.includes(t)), ...edits.toolsOn.filter(t => !initialDraft.tools.includes(t))],
    revokeDelegations: [...initialDraft.revokeDelegations, ...edits.revokeDelegations],
    revokeDormant: [...initialDraft.revokeDormant, ...edits.revokeDormant],
  }), [initialDraft, edits])
  const changes = useMemo(() => diffPermissions(initialDraft, draft, dormantScopes), [initialDraft, draft, dormantScopes])
  const counts = changeCounts(changes)
  const dirty = counts.grant + counts.revoke > 0
  // Neither a save in flight nor unsaved changes are thrown away by a stray click beside the dialog.
  const backdrop = useBackdropClose(() => { if (!saving && !dirty) onClose() })

  const setBoard = (id: string, on: boolean) => setEdits(e => {
    const was = initialDraft.boards.includes(id)
    return { ...e, boardsOn: on && !was ? [...without(e.boardsOn, id), id] : without(e.boardsOn, id), boardsOff: !on && was ? [...without(e.boardsOff, id), id] : without(e.boardsOff, id) }
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
      setEdits({
        children,
        boardsOn: f.grantBoards, boardsOff: f.revokeBoards,
        toolsOn: f.grantTools.map(t => toolKey(t.connection_id, t.tool)), toolsOff: f.revokeTools.map(t => toolKey(t.connection_id, t.tool)),
        revokeDelegations: f.revokeDelegations, revokeDormant: f.revokeDormant,
      })
      setFailedKeys([
        ...f.children.map(c => `c:${c.runtime}`),
        ...[...f.grantBoards, ...f.revokeBoards].map(b => `b:${b}`),
        ...[...f.grantTools, ...f.revokeTools].map(t => `t:${toolKey(t.connection_id, t.tool)}`),
        ...f.revokeDelegations.map(k => `d:${k}`), ...f.revokeDormant.map(id => `x:${id}`),
      ])
      setError(outcome.message)
    } catch (e) { setError(e instanceof Error ? e.message : 'Could not save the permissions.') }
    finally { setSaving(false) }
  }
  const notSaved = (key: string) => failedKeys.includes(key) ? <span className="pm-row-note pm-failed-note">Not saved</span> : null

  // ── What the search leaves ──
  const shownChildren = childOptions.filter(o => matchesQuery(query, o.label, engineNames(o.engines), 'children child'))
  const boardList = boards.state === 'ready' ? boards.value : []
  const shownBoards = boardList.filter(b => matchesQuery(query, b.name))
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
  const revoking = (a: ActiveEntry) => 'board' in a.target ? !draft.boards.includes(a.target.board)
    : 'tool' in a.target ? !draft.tools.includes(a.target.tool)
      : 'child' in a.target ? draft.children[a.target.child] !== 'on'
      : 'delegation' in a.target ? draft.revokeDelegations.includes(a.target.delegation) || changes.children.some(c => !c.enabled && 'delegation' in a.target && a.target.delegation.startsWith(`${c.runtime}\u0000`))
        : draft.revokeDormant.includes(a.target.dormant)
  const toggleRevoke = (a: ActiveEntry) => {
    if ('board' in a.target) setBoard(a.target.board, revoking(a))
    else if ('tool' in a.target) setTool(a.target.tool, revoking(a))
    else if ('child' in a.target) { const r = a.target.child; const o = childOptions.find(x => x.runtime === r); if (o) setChild(o) }
    else if ('delegation' in a.target) { const k = a.target.delegation; setEdits(e => ({ ...e, revokeDelegations: flip(e.revokeDelegations, k) })) }
    else { const k = a.target.dormant; setEdits(e => ({ ...e, revokeDormant: flip(e.revokeDormant, k) })) }
  }
  const additions: { key: string; kind: ActiveEntry['kind']; label: string; undo: () => void }[] = [
    ...childOptions.filter(o => draft.children[o.runtime] === 'on' && initialDraft.children[o.runtime] !== 'on').map(o => ({ key: `c:${o.runtime}`, kind: 'child' as const, label: o.label, undo: () => setChild(o) })),
    ...draft.boards.filter(b => !initialDraft.boards.includes(b)).map(b => ({ key: `b:${b}`, kind: 'board' as const, label: `File cards on ${boardName(b)} without asking`, undo: () => setBoard(b, false) })),
    ...draft.tools.filter(t => !initialDraft.tools.includes(t)).map(t => ({ key: `t:${t}`, kind: 'tool' as const, label: `Run ${toolName(t)} without asking`, undo: () => setTool(t, false) })),
  ]
  const shownActive = active.filter(a => matchesQuery(query, a.label, a.detail))
  const shownAdditions = additions.filter(a => matchesQuery(query, a.label))
  const pendingNote = mode === 'launch' ? 'Given as the session starts' : 'Applies on Save'

  const selectedBoards = draft.boards.filter(b => boardList.some(x => x.id === b)).length
  const shownBoardIds = shownBoards.map(b => b.id)
  const allShownSelected = shownBoardIds.length > 0 && shownBoardIds.every(id => draft.boards.includes(id))
  const nothing = (what: string) => <p className="pm-empty">{query ? `No ${what} match “${query.trim()}”.` : `No ${what}.`}</p>

  return createPortal(
    <div className="pm-overlay" data-testid="permissions-overlay" {...backdrop}>
      <div ref={dialogRef} className="pm-dialog" role="dialog" aria-modal="true" aria-labelledby={titleId} tabIndex={-1}
        data-testid="permissions-modal" onKeyDown={onKeyDown}>
        <header className="pm-head">
          <div className="pm-title">
            <h2 id={titleId}>Permissions</h2>
            <p>{mode === 'launch' ? 'What the new session may do without asking you. Given as it starts.' : 'What this session may do without asking you. Each one still runs every check of your approval, as you.'}</p>
          </div>
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

          {/* ── Board filing ── */}
          <section className="pm-section" data-section="boards" aria-labelledby={`${titleId}-boards`}>
            <div className="pm-section-head">
              <h3 id={`${titleId}-boards`}><span className="pm-glyph" aria-hidden="true">{KIND_GLYPH.board}</span>Board filing</h3>
              {canGrant && boards.state === 'ready' && boardList.length > 0 && <>
                <span className="pm-count" data-testid="pm-board-count">{selectedBoards} of {boardList.length} selected</span>
                <button type="button" className="pm-link" disabled={saving || shownBoardIds.length === 0} onClick={() => {
                  const next = toggleShown(draft.boards, shownBoardIds)
                  for (const id of shownBoardIds) setBoard(id, next.includes(id))
                }}>{allShownSelected ? 'Clear shown' : 'Select all shown'}</button>
              </>}
            </div>
            {!canGrant ? <p className="pm-empty" data-testid="pm-grant-reason">{grantReason || 'This session cannot be given these permissions.'}</p>
              : boards.state === 'loading' ? <p className="pm-empty" role="status">Checking your boards…</p>
                : boards.state === 'failed' ? <p className="pm-empty pm-error" role="alert">{boards.message} {boards.retry && <button type="button" className="perm-revoke" onClick={boards.retry}>Retry</button>}</p>
                  : boards.state === 'none' || boardList.length === 0 ? <p className="pm-empty">No boards to file on.</p>
                    : shownBoards.length === 0 ? nothing('boards')
                      : <ul className="pm-list pm-scroll" data-roving onKeyDown={onListKey} aria-label="Boards to file cards on without asking">
                        {shownBoards.map(b => {
                          const on = draft.boards.includes(b.id)
                          return <li key={b.id} className={`pm-row${on !== initialDraft.boards.includes(b.id) ? ' pm-changed' : ''}`}>
                            <label className="pm-check"><input type="checkbox" checked={on} disabled={saving} onChange={e => setBoard(b.id, e.target.checked)} aria-label={`File cards on ${b.name} without asking`} />
                              <span>{b.name}{notSaved(`b:${b.id}`)}</span></label>
                          </li>
                        })}
                      </ul>}
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
                      {notSaved('delegation' in a.target ? `d:${a.target.delegation}` : 'dormant' in a.target ? `x:${a.target.dormant}` : '')}<span className="pm-row-note">{out ? 'Revoked on Save' : a.detail}{a.dormant && !out && <span className="pm-warn"> · {a.dormant}</span>}</span></span>
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
