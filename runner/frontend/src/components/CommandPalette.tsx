import { useCallback, useEffect, useId, useMemo, useRef, useState, useSyncExternalStore, type ReactNode, type RefObject } from 'react'
import { createPortal, flushSync } from 'react-dom'
import { signedInAccount } from '../lib/liveBoardCard'
import { useLocation } from 'react-router-dom'
import { PaletteContext, usePalette } from '../commands/context'
import { CommandRegistry, assertCommandSafety, paletteShortcut, parseScope, paletteGroups, groupLabels, type PaletteGroup, type PaletteItem, type PaletteMode } from '../commands/registry'
import { FileIcon } from '@blerglab/chat'
import './CommandPalette.css'
import PaneProvider from './PaneProvider'
import { visibleSurfaceIsOpen, inShellTerminal, shellPaletteShortcut } from '../commands/shortcutOwnership'

// Existing dialogs keep their own focus trap and Esc handling. Check every entry point,
// including feature actions and the phone button, before mounting another modal.
const anotherModalIsOpen = () => visibleSurfaceIsOpen('[aria-modal="true"], dialog[open]', '.palette-dialog')

function ownsPaletteShortcut(event: KeyboardEvent) {
  if (inShellTerminal(event) && !shellPaletteShortcut(event)) return true
  // Ordinary text fields use the app shortcuts. Terminals and code editors keep
  // their native bindings, including when their input lives inside a shadow root.
  const surfaces = '[data-palette-shortcuts="ignore"], .xterm:not(.shell-terminal .xterm), .monaco-editor, .cm-editor, .CodeMirror, .ace_editor'
  return [...event.composedPath(), document.activeElement].some(target => target instanceof Element && !!target.closest(surfaces))
}

export function PaletteButton({ triggerRef }: { triggerRef?: RefObject<HTMLButtonElement | null> } = {}) {
  const palette = usePalette()
  return <button ref={triggerRef} type="button" className="icon-btn" aria-label="Search commands and files" title="Search commands and files (Ctrl/⌘+K)" onClick={() => palette?.open('commands')}><svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.75" aria-hidden="true"><circle cx="10.5" cy="10.5" r="6.5" /><path d="m16 16 5 5" /></svg></button>
}

export default function CommandPaletteProvider({ children }: { children: ReactNode }) {
  const [registry] = useState(() => new CommandRegistry())
  const [request, setRequest] = useState<{ mode: PaletteMode; n: number; route: string } | null>(null)
  const location = useLocation()
  const open = useCallback((mode: PaletteMode) => {
    if (anotherModalIsOpen()) return
    setRequest(old => ({ mode, n: (old?.n ?? 0) + 1, route: location.key }))
  }, [location.key])
  const close = useCallback(() => setRequest(null), [])
  const context = useMemo(() => ({ registry, open }), [registry, open])
  useEffect(() => {
    const claimed = new WeakSet<KeyboardEvent>()
    const captureKey = (event: KeyboardEvent) => {
      const mode = paletteShortcut(event, location.pathname)
      if (!mode || event.defaultPrevented || ownsPaletteShortcut(event)) return
      // Claim the browser binding even when a visible modal prevents opening,
      // or a local handler stops propagation before the event reaches window.
      event.preventDefault()
      // Each repeat has its own browser default, but must not reopen/reset search.
      if (event.repeat || anotherModalIsOpen()) return
      if (event.composedPath().some(target => target instanceof Element && target.matches('.agent-composer textarea, .shell-terminal .xterm-helper-textarea'))) {
        event.stopPropagation()
        open(mode)
      } else claimed.add(event)
    }
    const bubbleKey = (event: KeyboardEvent) => {
      // A local surface can stop propagation; defaultPrevented is already ours.
      if (!claimed.has(event)) return
      const mode = paletteShortcut(event, location.pathname)
      if (!mode) return
      event.stopImmediatePropagation()
      open(mode)
    }
    window.addEventListener('keydown', captureKey, true)
    window.addEventListener('keydown', bubbleKey)
    return () => { window.removeEventListener('keydown', captureKey, true); window.removeEventListener('keydown', bubbleKey) }
  }, [location.pathname, open])
  return <PaletteContext.Provider value={context}>
    <PaneProvider scope={location.pathname}>{children}
    {/* Navigation unmounts the current dialog and cancels its provider requests. */}
    {request?.route === location.key && <PaletteDialog key={request.n} registry={registry} mode={request.mode} close={close} />}
    </PaneProvider>
  </PaletteContext.Provider>
}

/** How many sessions the empty palette offers. */
const RECENT_SESSIONS = 3
// Two surfaces can register the same action (one id): show it once.
const unique = (items: PaletteItem[]) => [...new Map(items.map(item => [item.id, item])).values()]

export function PaletteDialog({ registry, mode, close }: { registry: CommandRegistry; mode: PaletteMode; close(): void }) {
  const snapshot = useSyncExternalStore(registry.subscribe, registry.getSnapshot)
  const [query, setQuery] = useState('')
  const [scope, setScope] = useState<PaletteGroup | null>(mode === 'files' ? 'files' : null)
  const [expanded, setExpanded] = useState<PaletteGroup[]>([])
  const search = query.trim()
  const commands = scope !== 'files'
  const [result, setResult] = useState<{ key: string; items: PaletteItem[]; note: string; busy: boolean }>({ key: '', items: [], note: '', busy: false })
  const [selected, setSelected] = useState<string | null>(null)
  const root = useRef<HTMLDivElement>(null)
  const input = useRef<HTMLInputElement>(null)
  const id = useId()
  const [account] = useState(signedInAccount)
  const currentKey = `${scope}:${search}`
  const providers = useMemo(() => snapshot.providers.filter(p => scope ? p.mode === scope : !!search && (p.mode !== 'cards' || search.length >= 3)), [snapshot.providers, scope, search])
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null
    const overflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    input.current?.focus()
    return () => { document.body.style.overflow = overflow; if (previous?.isConnected) previous.focus() }
  }, [])
  useEffect(() => {
    const changed = () => { if (signedInAccount() !== account) close() }
    window.addEventListener('blerg-identity-changed', changed)
    return () => window.removeEventListener('blerg-identity-changed', changed)
  }, [account, close])
  useEffect(() => {
    const controller = new AbortController()
    const timer = window.setTimeout(() => {
      setResult({ key: currentKey, items: [], note: '', busy: true })
      void Promise.allSettled(providers.map(p => p.search(search, controller.signal))).then(results => {
        if (controller.signal.aborted) return
        if (signedInAccount() !== account) { close(); return }
        const items: PaletteItem[] = [], notes: string[] = []
        for (const [index, result] of results.entries()) {
          if (result.status === 'fulfilled') {
            const rows = result.value.items.map(item => ({ ...item, group: item.group ?? providers[index].mode }))
            try { assertCommandSafety(rows.filter(item => item.group === 'commands')) }
            catch { notes.push('Some search actions are unavailable.'); continue }
            items.push(...rows)
            if (result.value.note) notes.push(result.value.note)
          }
          else notes.push(result.reason instanceof Error ? result.reason.message : 'Search is unavailable. Try again.')
        }
        if (scope === 'files' && providers.length === 0) notes.push('File search is unavailable for this session.')
        setResult({ key: currentKey, items, note: notes.join(' '), busy: false })
      })
    }, 150)
    return () => { window.clearTimeout(timer); controller.abort() }
  }, [providers, search, scope, currentKey, account, close])
  const pool = [...snapshot.items, ...(result.key === currentKey ? result.items : [])]
  const empty = !scope && !search
  // Before the person types: actions on this session first, a few live sessions they opened, then
  // the common commands. Typing searches everything, as before.
  const ranked = empty ? [] : registry.rank(pool.filter(item => {
    if (item.refine !== undefined) return false
    const group = item.group ?? 'commands'
    if (scope) return group === scope
    return group !== 'cards' || search.length >= 3
  }), search)
  const groups: { key: string; label: string; items: PaletteItem[] }[] = empty ? [
    { key: 'here', label: 'This session', items: unique(pool.filter(item => item.here !== undefined)).sort((a, b) => a.here! - b.here! || a.title.localeCompare(b.title)) },
    { key: 'sessions', label: 'Recent sessions', items: unique(pool.filter(item => item.group === 'sessions' && item.recentRank !== undefined)).sort((a, b) => a.recentRank! - b.recentRank!).slice(0, RECENT_SESSIONS) },
    { key: 'commands', label: groupLabels.commands, items: registry.rank(pool.filter(item => (item.group ?? 'commands') === 'commands' && item.empty && !item.destructive && item.here === undefined && item.refine === undefined), '') },
  ].filter(group => group.items.length) : paletteGroups.map(group => {
    const all = ranked.filter(item => (item.group ?? 'commands') === group)
    const visible = expanded.includes(group) ? all : all.slice(0, 5)
    const more: PaletteItem | null = all.length > visible.length ? {
      id: `more:${group}`, title: `More ${groupLabels[group].toLowerCase()} (${all.length - visible.length})`,
      run: () => { setExpanded(old => [...old, group]); setSelected(all.slice(visible.length).find(item => !item.destructive)?.id ?? null) },
    } : null
    return { key: group, label: groupLabels[group], items: more ? [...visible, more] : visible }
  }).filter(group => group.items.length)
  const items = groups.flatMap(group => group.items)
  const explicit = items.findIndex(item => item.id === selected)
  const active = explicit >= 0 ? explicit : items.findIndex(item => !item.destructive)
  const activeId = items[active]?.id
  const activeOptionId = activeId === undefined ? undefined : `${id}-${encodeURIComponent(activeId)}`
  useEffect(() => { if (activeOptionId) document.getElementById(activeOptionId)?.scrollIntoView?.({ block: 'nearest' }) }, [activeOptionId])
  const run = (item: PaletteItem) => {
    if (signedInAccount() !== account) { close(); return }
    if (item.id.startsWith('more:')) { item.run(); return }
    if (item.refine !== undefined) { setQuery(item.refine); setSelected(null); setExpanded([]); input.current?.focus(); return }
    if (!item.destructive) registry.remember(item.id)
    // Commit the close and restore focus before another feature opens its dialog.
    flushSync(close)
    item.run()
  }
  return createPortal(<div className="palette-overlay" onPointerDown={event => { if (event.target === event.currentTarget) close() }}>
    <div ref={root} className="palette-dialog" role="dialog" aria-modal="true" aria-labelledby={`${id}-title`} onKeyDown={event => {
      if (event.nativeEvent.isComposing) return
      if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close() }
      if (event.key === 'Tab') {
        event.preventDefault()
        const targets = [...(root.current?.querySelectorAll<HTMLElement>('button, input') ?? [])]
        const index = targets.indexOf(document.activeElement as HTMLElement)
        targets[(index + (event.shiftKey ? -1 : 1) + targets.length) % targets.length]?.focus()
      }
      if (event.target !== input.current) return
      if (event.key === 'Backspace' && !query && scope) { event.preventDefault(); setScope(null); setSelected(null); setExpanded([]) }
      if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
        event.preventDefault()
        const next = active < 0 ? (event.key === 'ArrowDown' ? 0 : items.length - 1) : (active + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % Math.max(1, items.length)
        setSelected(items[next]?.id ?? null)
      }
      if (event.key === 'Enter' && items[active]) { event.preventDefault(); run(items[active]) }
    }}>
      <header className="palette-header"><h2 id={`${id}-title`}>{commands ? 'Command palette' : 'Quick open'}</h2><button type="button" className="icon-btn" aria-label="Close search" onClick={close}>✕</button></header>
      <div className="palette-search">
        {scope && <button className="palette-chip" type="button" aria-label={`Remove ${groupLabels[scope].toLowerCase()} scope`} onClick={() => { setScope(null); setSelected(null); setExpanded([]); input.current?.focus() }}>{groupLabels[scope]} ×</button>}
        <input ref={input} className="palette-input" role="combobox" aria-label={scope === 'files' ? 'Search session files' : scope === 'sessions' ? 'Search sessions' : scope === 'cards' ? 'Search cards' : scope === 'commands' ? 'Search commands' : 'Search commands, sessions, cards and files'} aria-expanded="true" aria-autocomplete="list" aria-controls={`${id}-list`} aria-activedescendant={activeOptionId} aria-describedby={`${id}-hint`} placeholder="s sessions · # cards · / files · > commands" value={query} maxLength={256} onChange={event => {
          const parsed = parseScope(event.target.value)
          if (parsed.scope) { setScope(parsed.scope); setQuery(parsed.query) }
          else setQuery(event.target.value)
          setSelected(null); setExpanded([])
        }} />
      </div>
      <div className="palette-results" role="listbox" id={`${id}-list`} aria-label="Search results" aria-busy={result.busy && result.key === currentKey}>
        {groups.map(({ key: group, label, items: rows }) => <div role="group" aria-labelledby={`${id}-group-${group}`} key={group}>
          <h3 className="palette-group-title" id={`${id}-group-${group}`}>{label}</h3>
          {rows.map(item => <div role="option" id={`${id}-${encodeURIComponent(item.id)}`} key={item.id} aria-selected={item.id === activeId} className={`palette-option${item.destructive ? ' palette-destructive' : ''}${item.id.startsWith('more:') ? ' palette-more' : ''}`} onPointerDown={event => event.preventDefault()} onClick={() => run(item)} onPointerMove={() => setSelected(item.id)}>
            <span className="palette-label"><span>{group === 'files' ? <FileIcon name={item.title} /> : item.icon} {item.title}</span>{(item.repo || item.detail) && <small>{[item.repo, item.detail].filter(Boolean).join(' · ')}</small>}</span>{item.shortcut && <kbd>{item.shortcut}</kbd>}
          </div>)}
        </div>)}
      </div>
      <div className="palette-status" role="status" aria-live="polite">{result.key !== currentKey || result.busy ? 'Searching…' : result.note || (items.length ? `${empty ? items.length : ranked.length} results` : 'No matches.')}</div>
      <footer id={`${id}-hint`} className="palette-hint">↑ ↓ to choose · Enter to open · Esc to close · s sessions · # cards · / files · &gt; commands</footer>
    </div>
  </div>, document.body)
}
