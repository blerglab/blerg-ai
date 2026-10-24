import { useCallback, useEffect, useId, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from 'react'
import { createPortal, flushSync } from 'react-dom'
import { signedInAccount } from '../lib/liveBoardCard'
import { useLocation } from 'react-router-dom'
import { PaletteContext, usePalette } from '../commands/context'
import { CommandRegistry, paletteShortcut, type PaletteItem, type PaletteMode } from '../commands/registry'
import './CommandPalette.css'

// Existing dialogs keep their own focus trap and Esc handling. Check every entry point,
// including feature actions and the phone button, before mounting another modal.
function anotherModalIsOpen() {
  return [...document.querySelectorAll('[aria-modal="true"], dialog[open]')].some(modal =>
    !modal.closest('.palette-dialog') && !modal.closest('[hidden], [aria-hidden="true"]') && getComputedStyle(modal).display !== 'none')
}

function ownsTextInput(event: KeyboardEvent) {
  const editable = 'input, textarea, select, [contenteditable]:not([contenteditable="false"]), [role="textbox"], [role="searchbox"], [role="combobox"]'
  return [...event.composedPath(), document.activeElement].some(target => target instanceof Element && !!target.closest(editable))
}

export function PaletteButton() {
  const palette = usePalette()
  return <button type="button" className="icon-btn" aria-label="Search commands and files" title="Search commands and files (Ctrl/⌘+K)" onClick={() => palette?.open('commands')}><svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.75" aria-hidden="true"><circle cx="10.5" cy="10.5" r="6.5" /><path d="m16 16 5 5" /></svg></button>
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
    const key = (event: KeyboardEvent) => {
      const mode = paletteShortcut(event, location.pathname)
      if (!mode || event.defaultPrevented || ownsTextInput(event) || anotherModalIsOpen()) return
      event.preventDefault()
      event.stopImmediatePropagation()
      open(mode)
    }
    // Bubble after the focused feature has had a chance to claim its shortcuts.
    window.addEventListener('keydown', key)
    return () => window.removeEventListener('keydown', key)
  }, [location.pathname, open])
  return <PaletteContext.Provider value={context}>
    {children}
    {/* Navigation unmounts the current dialog and cancels its provider requests. */}
    {request?.route === location.key && <PaletteDialog key={request.n} registry={registry} mode={request.mode} close={close} />}
  </PaletteContext.Provider>
}

export function PaletteDialog({ registry, mode, close }: { registry: CommandRegistry; mode: PaletteMode; close(): void }) {
  const snapshot = useSyncExternalStore(registry.subscribe, registry.getSnapshot)
  const [query, setQuery] = useState(mode === 'commands' ? '>' : '')
  const commands = query.startsWith('>')
  const search = commands ? query.slice(1).trimStart() : query
  const [result, setResult] = useState<{ key: string; items: PaletteItem[]; note: string; busy: boolean }>({ key: '', items: [], note: '', busy: false })
  const [selected, setSelected] = useState(0)
  const root = useRef<HTMLDivElement>(null)
  const input = useRef<HTMLInputElement>(null)
  const id = useId()
  const [account] = useState(signedInAccount)
  const currentKey = `${commands}:${search}`
  const providers = useMemo(() => snapshot.providers.filter(p => p.mode === (commands ? 'commands' : 'files')), [snapshot.providers, commands])
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null
    const overflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    input.current?.focus()
    return () => { document.body.style.overflow = overflow; if (previous?.isConnected) previous.focus() }
  }, [])
  useEffect(() => {
    const controller = new AbortController()
    const timer = window.setTimeout(() => {
      setResult({ key: currentKey, items: [], note: '', busy: true })
      void Promise.allSettled(providers.map(p => p.search(search, controller.signal))).then(results => {
        if (controller.signal.aborted) return
        if (signedInAccount() !== account) { close(); return }
        const items: PaletteItem[] = [], notes: string[] = []
        for (const result of results) {
          if (result.status === 'fulfilled') { items.push(...result.value.items); if (result.value.note) notes.push(result.value.note) }
          else notes.push(result.reason instanceof Error ? result.reason.message : 'Search is unavailable. Try again.')
        }
        if (!commands && providers.length === 0) notes.push('File search is unavailable for this session.')
        setResult({ key: currentKey, items, note: notes.join(' '), busy: false })
      })
    }, 150)
    return () => { window.clearTimeout(timer); controller.abort() }
  }, [providers, search, commands, currentKey, account, close])
  const items = registry.rank([...(commands ? snapshot.items : []), ...(result.key === currentKey ? result.items : [])], search)
  const active = Math.min(selected, Math.max(0, items.length - 1))
  useEffect(() => { document.getElementById(`${id}-${active}`)?.scrollIntoView?.({ block: 'nearest' }) }, [id, active])
  const run = (item: PaletteItem) => {
    if (signedInAccount() !== account) { close(); return }
    registry.remember(item.id)
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
      if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault(); setSelected((active + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % Math.max(1, items.length)) }
      if (event.key === 'Enter' && items[active]) { event.preventDefault(); run(items[active]) }
    }}>
      <header className="palette-header"><h2 id={`${id}-title`}>{commands ? 'Command palette' : 'Quick open'}</h2><button type="button" className="icon-btn" aria-label="Close search" onClick={close}>✕</button></header>
      <input ref={input} className="palette-input" role="combobox" aria-label={commands ? 'Search commands' : 'Search session files'} aria-expanded="true" aria-autocomplete="list" aria-controls={`${id}-list`} aria-activedescendant={items[active] ? `${id}-${active}` : undefined} aria-describedby={`${id}-hint`} placeholder="Search files, or type > for commands" value={query} maxLength={256} onChange={event => { setQuery(event.target.value); setSelected(0) }} />
      <div className="palette-results" role="listbox" id={`${id}-list`} aria-label={commands ? 'Commands' : 'Session files'} aria-busy={result.busy && result.key === currentKey}>
        {items.map((item, index) => <div role="option" id={`${id}-${index}`} key={item.id} aria-selected={index === active} className="palette-option" onPointerDown={event => event.preventDefault()} onClick={() => run(item)} onPointerMove={() => setSelected(index)}>
          <span className="palette-label"><span>{item.title}</span>{item.detail && <small>{item.detail}</small>}</span>{item.shortcut && <kbd>{item.shortcut}</kbd>}
        </div>)}
      </div>
      <div className="palette-status" role="status" aria-live="polite">{result.key !== currentKey || result.busy ? 'Searching…' : result.note || (items.length ? `${items.length} results` : 'No matches.')}</div>
      <footer id={`${id}-hint`} className="palette-hint">↑ ↓ to choose · Enter to open · Esc to close{!commands && ' · > for commands'}</footer>
    </div>
  </div>, document.body)
}
