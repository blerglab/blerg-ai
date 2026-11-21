import { useToastStore } from '../hooks/useToastStore'
import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import type { ReactNode } from 'react'
import type { SessionTabs } from '../hooks/useSessionTabs'
import { layoutLibraryKey, readLayoutLibrary, writeLayoutLibrary, type LayoutLibrary, type NamedLayout, type WorkspaceLayout } from '../lib/workspaceLayouts'
import LayoutPreview from './LayoutPreview'
import { layoutSummary, mainOnlyLayout } from '../lib/layoutPreview'
import './WorkspaceLayouts.css'
import { IconMenuItem, PanelToggleIcon } from './IconButton'

export default function WorkspaceLayouts({ account, tabs, menu = false }: { account: string; tabs: SessionTabs; menu?: boolean }) {
  const [open, setOpen] = useState(false)
  const [returnFocus, setReturnFocus] = useState<HTMLElement | null>(null)
  const show = () => {
    const active = document.activeElement
    setReturnFocus(active instanceof HTMLElement ? active.closest('.icon-menu')?.querySelector<HTMLButtonElement>('button[aria-haspopup="menu"]') ?? active : null)
    setOpen(true)
  }
  return <>{menu ? <IconMenuItem label="Layouts" icon={<PanelToggleIcon />} onSelect={show} /> : <button type="button" className="icon-btn" aria-label="Layouts" title="Layouts" onClick={show}><svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.75" aria-hidden="true"><rect x="3" y="3" width="18" height="18" rx="2" /><path d="M9 3v18M9 10h12" /></svg></button>}{open && <LayoutDialog key={account + tabs.sessionId} account={account} tabs={tabs} returnFocus={returnFocus} close={() => setOpen(false)} />}</>
}
function SavedTime({ value, automatic = false }: { value?: number; automatic?: boolean }) {
  const date = new Date(value ?? NaN)
  return Number.isFinite(date.getTime()) ? <time dateTime={date.toISOString()}>{automatic ? 'Updated' : 'Saved'} {date.toLocaleString('en-US', { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })}</time> : <span className="layout-card-time">{automatic ? 'Automatic · this workspace' : 'Saved earlier'}</span>
}
function LayoutCard({ name, layout, savedAt, automatic, lastUsed, children }: { name: string; layout?: WorkspaceLayout; savedAt?: number; automatic?: boolean; lastUsed?: boolean; children: ReactNode }) {
  const summary = layout ? layoutSummary(layout) : 'Your next working layout will appear here.'
  return <article className={`workspace-layout-card${automatic ? ' automatic' : ''}${lastUsed ? ' last-used' : ''}`} aria-label={name}>
    <div className="layout-card-preview"><LayoutPreview layout={layout ?? mainOnlyLayout} name={name} /></div>
    <div className="layout-card-details">
      <div className="layout-card-heading"><h3>{name}</h3>{lastUsed && <span className="layout-card-badge">Last used</span>}{automatic && <span className="layout-card-auto">Automatic</span>}</div>
      <p className="layout-card-summary" title={summary}>{summary}</p>
      <SavedTime value={savedAt ?? layout?.savedAt} automatic={automatic} />
      <div className="layout-card-actions">{children}</div>
    </div>
  </article>
}
function LayoutDialog({ account, tabs, returnFocus, close }: { account: string; tabs: SessionTabs; returnFocus: HTMLElement | null; close(): void }) {
  const [library, setLibrary] = useState(() => readLayoutLibrary(account))
  const [name, setName] = useState('')
  const [error, setError] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [editing, setEditing] = useState<string | null>(null)
  const [justSaved, setJustSaved] = useState<string | null>(null)
  const input = useRef<HTMLInputElement>(null)
  const list = useRef<HTMLDivElement>(null)
  const [recovery] = useState(() => tabs.getLastLayout())
  useEffect(() => {
    const previous = returnFocus ?? document.activeElement as HTMLElement | null
    input.current?.focus()
    const refresh = (event: StorageEvent) => { if (event.key === layoutLibraryKey(account)) setLibrary(readLayoutLibrary(account)) }
    window.addEventListener('storage', refresh)
    return () => { window.removeEventListener('storage', refresh); if (previous?.isConnected) previous.focus() }
  }, [account, returnFocus])
  useEffect(() => { if (justSaved) list.current?.querySelector<HTMLElement>(`[data-layout-id="${justSaved}"]`)?.scrollIntoView?.({ block: 'nearest' }) }, [justSaved])
  const save = (next: LayoutLibrary) => {
    if (!writeLayoutLibrary(account, next)) { setError('Could not save layouts in this browser.'); return false }
    setLibrary(next); setError(''); return true
  }
  const remove = (entry: NamedLayout) => {
    const previous = library
    if (!save({ ...library, entries: library.entries.filter(value => value.id !== entry.id), lastUsed: library.lastUsed === entry.id ? undefined : library.lastUsed })) return
    setConfirmation('')
    useToastStore.getState().addToast({ title: 'Layout deleted', body: entry.name, action: { label: 'Undo', onClick: () => {
      const current = readLayoutLibrary(account), entries = [...current.entries]
      if (!entries.some(value => value.id === entry.id)) entries.splice(Math.min(previous.entries.findIndex(value => value.id === entry.id), entries.length), 0, entry)
      const restored = { entries, lastUsed: current.lastUsed ?? previous.lastUsed }
      if (writeLayoutLibrary(account, restored)) setLibrary(restored)
    } } })
  }
  return createPortal(<div className="workspace-layout-backdrop" onClick={event => { if (event.target === event.currentTarget) close() }}>
    <section className="workspace-layout-dialog" role="dialog" aria-modal="true" aria-label="Layouts" onKeyDown={event => {
      event.stopPropagation()
      if (event.key === 'Escape') { event.preventDefault(); close() }
      if (event.key === 'Tab') {
        const items = [...event.currentTarget.querySelectorAll<HTMLElement>('input, button:not(:disabled)')]
        if (event.shiftKey && document.activeElement === items[0]) { event.preventDefault(); items.at(-1)?.focus() }
        if (!event.shiftKey && document.activeElement === items.at(-1)) { event.preventDefault(); items[0]?.focus() }
      }
    }}>
      <header><div><h2>Layouts</h2><p>Pick up where you left off.</p></div><button type="button" className="icon-btn" aria-label="Close layouts" title="Close layouts" onClick={close}>×</button></header>
      <form className="workspace-layout-save" onSubmit={event => {
        event.preventDefault()
        if (!name.trim() || library.entries.length >= 50) return
        const id = crypto.randomUUID(), savedAt = Date.now(), title = name.trim()
        if (save({ entries: [{ id, name: title, savedAt, layout: tabs.captureLayout() }, ...library.entries], lastUsed: id })) { setName(''); setConfirmation(`Saved “${title}”.`); setJustSaved(id) }
      }}>
        <label>Save current layout<input ref={input} aria-label="Layout name" placeholder="e.g., Research" maxLength={80} value={name} onChange={event => setName(event.target.value)} /></label>
        <button className="btn primary" disabled={!name.trim() || library.entries.length >= 50}>Save</button>
        <span className="layout-save-confirmation" role="status">{confirmation || 'Saved in this browser for your account.'}</span>
      </form>
      {error && <p role="alert">{error}</p>}
      <div className="workspace-layout-list" ref={list}>
        <div className="layout-card-holder"><LayoutCard name="Recover last layout" layout={recovery} automatic>
          <button type="button" className="btn primary" aria-label="Load last layout" disabled={!recovery} onClick={() => { if (tabs.restoreLayout()) close() }}>Load</button>
        </LayoutCard></div>
        {library.entries.map(entry => <div className="layout-card-holder" key={entry.id} data-layout-id={entry.id}>
          <LayoutCard name={entry.name} layout={entry.layout} savedAt={entry.savedAt} lastUsed={library.lastUsed === entry.id}>
            <button type="button" className="btn primary" aria-label={`Load ${entry.name}`} onClick={() => { if (tabs.applyLayout(entry.layout)) { save({ ...library, lastUsed: entry.id }); close() } }}>Load</button>
            {editing === entry.id ? <input autoFocus className="layout-rename" aria-label={`Rename ${entry.name}`} defaultValue={entry.name} maxLength={80} onBlur={event => {
              const title = event.target.value.trim()
              if (title && title !== entry.name) save({ ...library, entries: library.entries.map(value => value.id === entry.id ? { ...value, name: title } : value) })
              setEditing(null)
            }} onKeyDown={event => { if (event.key === 'Enter') { event.preventDefault(); event.currentTarget.blur() } if (event.key === 'Escape') { event.stopPropagation(); setEditing(null) } }} /> : <button type="button" className="layout-card-action" title={`Rename ${entry.name}`} aria-label={`Rename ${entry.name}`} onClick={() => setEditing(entry.id)}>Rename</button>}
            <button type="button" className="layout-card-action" title={`Delete ${entry.name}`} aria-label={`Delete ${entry.name}`} onClick={() => remove(entry)}>Delete</button>
          </LayoutCard>
        </div>)}
        {!library.entries.length && <div className="workspace-layout-empty"><strong>Make this workspace yours.</strong><p>Save an arrangement you like, then bring it back with one click.</p></div>}
      </div>
      <footer><div><button type="button" className="workspace-layout-reset" onClick={() => { tabs.resetLayout(); close() }}>Reset layout</button><p>Back to Main only, nothing else open.</p></div><span>You can undo this.</span></footer>
    </section>
  </div>, document.body)
}
