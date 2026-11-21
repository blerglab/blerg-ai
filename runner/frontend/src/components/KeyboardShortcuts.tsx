import { useEffect, useId, useRef, useState, useSyncExternalStore } from 'react'
import { createPortal } from 'react-dom'
import { shortcutGroups, type CommandRegistry } from '../commands/registry'
import '../../../../packages/settings/settings.css'
import './CommandPalette.css'
import { platformShortcut } from '../commands/shortcuts'
import { signedInAccount } from '../lib/liveBoardCard'

export default function KeyboardShortcuts({ registry, close }: { registry: CommandRegistry; close(): void }) {
  const snapshot = useSyncExternalStore(registry.subscribe, registry.getSnapshot)
  const [query, setQuery] = useState('')
  const input = useRef<HTMLInputElement>(null)
  const root = useRef<HTMLDivElement>(null)
  const id = useId()
  useEffect(() => {
    const account = signedInAccount()
    const changed = () => { if (signedInAccount() !== account) close() }
    window.addEventListener('blerg-identity-changed', changed)
    return () => window.removeEventListener('blerg-identity-changed', changed)
  }, [close])
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null
    const overflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    input.current?.focus()
    return () => { document.body.style.overflow = overflow; if (previous?.isConnected) previous.focus() }
  }, [])
  const items = [...new Map(snapshot.items.filter(item => item.shortcut).map(item => [item.id, item])).values()]
    .filter(item => `${item.title} ${item.shortcutGroup ?? 'In a session'} ${platformShortcut(item.shortcut!)}`.toLowerCase().includes(query.trim().toLowerCase()))
  return createPortal(<div className="palette-overlay" onPointerDown={event => { if (event.target === event.currentTarget) close() }}>
    <div ref={root} className="palette-dialog shortcuts-dialog" role="dialog" aria-modal="true" aria-labelledby={`${id}-title`} onKeyDown={event => {
      if (event.nativeEvent.isComposing) return
      if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close() }
      if (event.key === 'Tab') {
        event.preventDefault()
        const targets = [...(root.current?.querySelectorAll<HTMLElement>('input, button') ?? [])]
        const index = targets.indexOf(document.activeElement as HTMLElement)
        targets[(index + (event.shiftKey ? -1 : 1) + targets.length) % targets.length]?.focus()
      }
    }}>
      <header className="palette-header"><h2 id={`${id}-title`}>Keyboard shortcuts</h2><button type="button" className="settings-action" aria-label="Close shortcuts" onClick={close}>✕</button></header>
      <div className="palette-search"><input ref={input} className="settings-field palette-input" type="search" aria-label="Search shortcuts" placeholder="Search shortcuts" value={query} onChange={event => setQuery(event.target.value)} /></div>
      <div className="palette-results">
        {shortcutGroups.map(group => {
          const rows = items.filter(item => (item.shortcutGroup ?? 'In a session') === group)
          if (!rows.length) return null
          return <section className="settings-section shortcuts-section" key={group} aria-labelledby={`${id}-${group}`}>
            <h3 className="settings-heading palette-group-title" id={`${id}-${group}`}>{group}</h3>
            <ul className="shortcuts-list">{rows.map(item => <li key={item.id}><span>{item.title}</span><kbd>{platformShortcut(item.shortcut!)}</kbd></li>)}</ul>
          </section>
        })}
        {!items.length && <p className="palette-status" role="status">No matching shortcuts.</p>}
      </div>
      <footer className="palette-hint">Shortcuts available in this view.</footer>
    </div>
  </div>, document.body)
}
