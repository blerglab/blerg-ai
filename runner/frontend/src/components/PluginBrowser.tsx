// PluginBrowser: what the Capabilities tab's "+ Add" opens (card #144). A dialog that uses the space
// (a large panel on desktop, a full-screen sheet on a phone) listing every plugin of the marketplaces
// the session's engine knows, with a filter and an Install per plugin. Installing is the existing
// owner-approved path (POST /api/sessions/{id}/plugins, scoped to the session or always on). An
// engine with no catalog (or a session the daemon cannot read one for) gets that engine's add form
// instead. Secondary actions: add a marketplace, or enter a plugin by name. All text is plain text.
import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useBackdropClose } from '@blerglab/chat'
import { apiFetch } from '../apiFetch'
import type { ProposalInfo } from '../types'
import type { useSessionPlugins } from '../hooks/useSessionPlugins'
import { pluginApplication, pluginState, type PluginScope } from '../lib/sessionPlugins'
import {
  addMarketplace, alwaysOn, fetchCatalog, installBlocker, matchesPlugin, validMarketplaceInput,
  type CatalogMarketplace, type CatalogPlugin, type CatalogResult,
} from '../lib/pluginCatalog'
import { PluginAddForm, StartPluginSession } from './SessionPlugins'
import './PluginBrowser.css'

type Model = ReturnType<typeof useSessionPlugins>

// The most rows drawn at once; the filter narrows the rest.
const SHOWN = 300
const FOCUSABLE = 'button:not([disabled]), input:not([disabled]), select:not([disabled]), [href], [tabindex]:not([tabindex="-1"])'

interface Props {
  model: Model
  onClose: () => void
}

export default function PluginBrowser({ model, onClose }: Props) {
  const titleId = useId()
  const dialogRef = useRef<HTMLDivElement | null>(null)
  const searchRef = useRef<HTMLInputElement | null>(null)
  const backdrop = useBackdropClose(onClose)
  const { sessionId, data } = model
  const [result, setResult] = useState<CatalogResult | null>(null)
  const [reload, setReload] = useState(0)
  const [query, setQuery] = useState('')
  const [scope, setScope] = useState<PluginScope>('session')
  const [market, setMarket] = useState('')
  const [byName, setByName] = useState(false)

  // Requests can finish on a later poll, after the turn that delayed application.
  // A stable completion key avoids refetching the native catalog on every poll.
  const completedRequests = JSON.stringify((data?.requests ?? [])
    .map(p => [p.id, pluginState(p)])
    .filter(([, state]) => ['applied', 'failed', 'new_sessions'].includes(state)))
  useEffect(() => {
    let live = true
    void fetchCatalog(sessionId).then(r => { if (live) setResult(r) })
    return () => { live = false }
  }, [sessionId, reload, completedRequests])

  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null
    ;(searchRef.current ?? dialogRef.current?.querySelector<HTMLElement>(FOCUSABLE))?.focus()
    return () => opener?.focus?.()
  }, [])
  // Focus the filter once the list arrives.
  const loaded = result?.kind === 'ok'
  useEffect(() => { if (loaded) searchRef.current?.focus() }, [loaded])

  // Escape closes the browser wherever the focus is (a button that went busy drops it to the page),
  // caught on the way down so the drawer under it stays open.
  const closeRef = useRef(onClose)
  useEffect(() => { closeRef.current = onClose }, [onClose])
  useEffect(() => {
    const esc = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      e.stopPropagation()
      e.preventDefault()
      closeRef.current()
    }
    document.addEventListener('keydown', esc, true)
    return () => document.removeEventListener('keydown', esc, true)
  }, [])

  function onKeyDown(e: React.KeyboardEvent) {
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

  const catalog = result?.kind === 'ok' ? result.catalog : null
  const markets = useMemo(() => new Map<string, CatalogMarketplace>((catalog?.marketplaces ?? []).map(m => [m.name, m])), [catalog])
  const q = query.trim()
  const filtered = useMemo(
    () => (catalog?.plugins ?? []).filter(p => (!market || p.marketplace === market) && matchesPlugin(p, q)),
    [catalog, market, q],
  )
  const effectiveScope: PluginScope = data?.session_apply === true ? scope : 'always'

  let body: React.ReactNode
  if (!result) {
    body = <p className="pb-empty" role="status">Loading the plugins your engine knows…</p>
  } else if (result.kind === 'fallback') {
    body = (
      <div className="pb-fallback" data-testid="plugin-browser-fallback">
        <p className="pb-note">
          {result.code === 'sandboxed'
            ? 'Browsing plugins is not available for sandboxed sessions yet. Add one by name:'
            : 'This engine has no plugin catalog here. Add one by name:'}
        </p>
        <PluginAddForm model={model} onStart={onClose} />
      </div>
    )
  } else if (result.kind === 'error') {
    body = (
      <p className="pb-empty" role="alert">
        {result.message}{' '}
        <button type="button" className="pb-link" onClick={() => { setResult(null); setReload(n => n + 1) }}>Try again</button>
      </p>
    )
  } else if (result.catalog.plugins.length === 0) {
    body = <p className="pb-empty" data-testid="plugin-browser-none">No marketplace with plugins is known yet. Add one below.</p>
  } else {
    body = (
      <>
        {result.catalog.truncated && (
          <p className="pb-truncated" role="status" data-testid="plugin-browser-truncated">
            Not every plugin is listed: the catalog is larger than Blerg shows. Search narrows what is here; enter a plugin by name for the rest.
          </p>
        )}
        <ul className="pb-list" data-testid="plugin-browser-list">
          {filtered.slice(0, SHOWN).map(p => (
            <PluginRow key={`${p.marketplace}\u0000${p.name}`} plugin={p} market={markets.get(p.marketplace)} model={model} scope={effectiveScope} onClose={onClose} />
          ))}
        </ul>
        {filtered.length === 0 && <p className="pb-empty" data-testid="plugin-browser-no-match">No plugins match “{q}”.</p>}
        {filtered.length > SHOWN && <p className="pb-note">Showing {SHOWN} of {filtered.length}. Narrow the filter to see the rest.</p>}
      </>
    )
  }

  return createPortal(
    <div className="caps-overlay pb-overlay" data-testid="plugin-browser-overlay" {...backdrop}>
      <div ref={dialogRef} className="pb-dialog" role="dialog" aria-modal="true" aria-labelledby={titleId} data-testid="plugin-browser" onKeyDown={onKeyDown}>
        <header className="caps-header">
          <h2 id={titleId}>Add a plugin</h2>
          <button type="button" className="caps-close" aria-label="Close" onClick={onClose}>✕</button>
        </header>
        {catalog && (
          <div className="pb-toolbar">
            <input ref={searchRef} className="caps-search pb-search" type="search" value={query} onChange={e => setQuery(e.target.value)}
              placeholder="Search plugins by name, description, author…" aria-label="Search plugins" />
            {catalog.marketplaces.length > 1 && (
              <select className="pb-select" aria-label="Marketplace" value={market} onChange={e => setMarket(e.target.value)}>
                <option value="">All marketplaces</option>
                {catalog.marketplaces.map(m => <option key={m.name} value={m.name}>{m.name}</option>)}
              </select>
            )}
            <select className="pb-select" aria-label="Install for" value={effectiveScope} onChange={e => setScope(e.target.value as PluginScope)}>
              <option value="session" disabled={data?.session_apply !== true}>This session only</option>
              <option value="always">Always on</option>
            </select>
            <span className="pb-count" data-testid="plugin-browser-count">{q || market ? `${filtered.length} of ${catalog.plugins.length}` : catalog.plugins.length}</span>
          </div>
        )}
        {catalog && data?.reason && effectiveScope === 'always' && <p className="pb-note pb-pad">{data.reason}</p>}
        <div className="pb-body">{body}</div>
        {catalog && (
          <footer className="pb-footer">
            <AddMarketplace sessionId={sessionId} onCatalog={r => setResult(r.kind === 'ok' ? r : result)} />
            <button type="button" className="pb-link" aria-expanded={byName} onClick={() => setByName(v => !v)}>
              {byName ? 'Hide the name form' : 'Enter a plugin by name'}
            </button>
            {byName && <PluginAddForm model={model} onStart={onClose} />}
          </footer>
        )}
      </div>
    </div>,
    document.body,
  )
}

function PluginRow({ plugin, market, model, scope, onClose }: { plugin: CatalogPlugin; market?: CatalogMarketplace; model: Model; scope: PluginScope; onClose: () => void }) {
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState<{ text: string; failed: boolean; newSession?: boolean } | null>(null)
  const removing = scope === 'session' && plugin.managed === true
  const blocker = installBlocker(market, model.data, removing) ?? (removing ? null : plugin.install_reason)
  const always = alwaysOn(market, plugin.name, model.data)
  const already = scope === 'session' ? plugin.enabled === true : always
  const disallowed = market?.source && model.data && !model.data.any_marketplace && !model.data.allowed_marketplaces.some(s => s.toLowerCase() === market.source?.toLowerCase())
  const state = plugin.enabled ? 'Enabled in this session' : plugin.installed ? 'Installed, not enabled here' : always ? 'Always on for new sessions' : ''
  async function install() {
    if (busy || blocker || !market?.source) return
    setBusy(true)
    setNote(null)
    try {
      const res = await apiFetch(`/api/sessions/${encodeURIComponent(model.sessionId)}/plugins`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ marketplace: market.source, plugin: plugin.name, scope, ...(removing ? { action: 'remove' } : {}) }),
      })
      if (!res.ok) throw new Error('refused')
      const proposal = (await res.json()) as ProposalInfo
      setNote({ text: pluginApplication(proposal), failed: false, newSession: (proposal.result as {state?: string} | null)?.state === 'new_sessions' })
      await model.refresh()
    } catch {
      setNote({ text: 'The plugin was not accepted. Refresh and try again.', failed: true })
    } finally {
      setBusy(false)
    }
  }
  const disabledReason = busy ? 'Saving the plugin change.' : blocker ?? (already && !removing ? scope === 'session' ? 'Already enabled in this session.' : 'Already always on for new sessions.' : null)
  const reasonId = useId()
  const meta = [plugin.version && `v${plugin.version.replace(/^v/, '')}`, plugin.author, plugin.category].filter(Boolean).join(' · ')
  return (
    <li className="pb-row" data-testid="plugin-browser-row">
      <div className="pb-row-main">
        <div className="pb-row-line">
          {state && (
            <span className={`pb-state${plugin.enabled ? ' on' : ''}`} title={state} data-testid="plugin-state">
              <span aria-hidden="true">{plugin.enabled ? '●' : plugin.installed ? '✓' : '∞'}</span><span className="rail-sr">{state}</span>
            </span>
          )}
          <span className="pb-name" title={plugin.name}>{plugin.name}</span>
          <span className="rail-chip pb-market" title={market?.source ? `Marketplace ${plugin.marketplace} (${market.source})` : `Marketplace ${plugin.marketplace}`}>{plugin.marketplace}</span>
          {meta && <span className="pb-meta" title={meta}>{meta}</span>}
        </div>
        {disallowed && (plugin.installed || plugin.enabled || always) && <p className="pb-note">Marketplace no longer allowed. Existing plugins are kept.</p>}
        {plugin.description && <p className="pb-desc" title={plugin.description}>{plugin.description}</p>}
        {note && <p className={`pb-note${note.failed ? ' failed' : ''}`} role={note.failed ? 'alert' : 'status'}>{note.text} {note.newSession && <StartPluginSession onStart={onClose} />}</p>}
      </div>
      <div className="pb-action">
        <button type="button" className="pb-install" disabled={!!disabledReason} aria-describedby={disabledReason ? reasonId : undefined}
          title={disabledReason ?? (removing ? 'Remove from this session between turns; account settings stay unchanged' : undefined)}
          aria-label={`${removing ? 'Remove' : 'Install'} ${plugin.name}`} onClick={() => void install()}>
          {busy ? 'Saving…' : removing ? 'Remove' : already ? scope === 'session' ? 'Enabled' : 'Always on' : 'Install'}
        </button>
        {disabledReason && <p id={reasonId} className="pb-disabled">{disabledReason}</p>}
      </div>
    </li>
  )
}

function AddMarketplace({ sessionId, onCatalog }: { sessionId: string; onCatalog: (r: CatalogResult) => void }) {
  const inputId = useId()
  const [source, setSource] = useState('')
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState<{ text: string; failed: boolean } | null>(null)
  const valid = validMarketplaceInput(source)
  async function add() {
    if (busy || !valid) return
    setBusy(true)
    setMsg(null)
    const r = await addMarketplace(sessionId, source.trim())
    setBusy(false)
    if (r.kind === 'ok') {
      setSource('')
      setMsg({ text: 'Marketplace added.', failed: false })
      onCatalog(r)
    } else {
      setMsg({ text: r.kind === 'error' ? r.message : 'Marketplaces cannot be added for this session.', failed: true })
    }
  }
  return (
    <form className="pb-add-market" onSubmit={e => { e.preventDefault(); void add() }}>
      <label htmlFor={inputId}>Add marketplace</label>
      <input id={inputId} value={source} maxLength={300} placeholder="owner/repo or https://github.com/owner/repo" autoCapitalize="none" spellCheck={false}
        onChange={e => setSource(e.target.value)} />
      <button type="submit" disabled={busy || !valid}>{busy ? 'Adding…' : 'Add'}</button>
      <p className="pb-note pb-shared">Marketplaces are shared by everyone using this machine's engine.</p>
      {msg && <p className={`pb-note${msg.failed ? ' failed' : ''}`} role={msg.failed ? 'alert' : 'status'}>{msg.text}</p>}
    </form>
  )
}
