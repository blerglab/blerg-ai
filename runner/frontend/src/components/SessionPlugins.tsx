import { useId, useState } from 'react'
import { Link } from 'react-router-dom'
import { apiFetch } from '../apiFetch'
import type { ProposalInfo } from '../types'
import { pluginApplication, type PluginScope } from '../lib/sessionPlugins'
import { useSessionPlugins } from '../hooks/useSessionPlugins'
import './SessionPlugins.css'
import { openLaunchSheet } from '../hooks/useLaunchSheetOpen'

type Model = ReturnType<typeof useSessionPlugins>

/** The Capabilities toolbar's "+ Add": opens the plugin browser (PluginBrowser), whose fallback is the add form. */
export function PluginAddButton({ open, onToggle, controls }: { open: boolean; onToggle: () => void; controls?: string }) {
  return (
    <button type="button" className="caps-add" aria-label="Add plugin or skill" aria-haspopup="dialog" aria-expanded={open} aria-controls={open ? controls : undefined}
      onClick={onToggle} data-testid="caps-add">
      <span className="caps-add-plus" aria-hidden="true">+</span> Add
    </button>
  )
}

/** Adding a plugin (skills come in plugin bundles) to the session or to the account: the form the
 *  toolbar's "+ Add" opens. */
export function PluginAddForm({ model, id, onStart }: { model: Model; id?: string; onStart?: () => void }) {
  const reasonId = useId()
  const { data, error, refresh, sessionId } = model
  const [marketplace, setMarketplace] = useState('')
  const [name, setName] = useState('')
  const [scope, setScope] = useState<PluginScope>('session')
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)
  const [applied, setApplied] = useState<{ session: string; text: string; newSession: boolean } | null>(null)
  const source = marketplace || data?.allowed_marketplaces[0] || ''
  const effectiveScope = data?.session_apply === true ? scope : 'always'
  const validName = /^[a-z0-9][a-z0-9._-]{0,63}$/.test(name)
  const nativeSource = /^codex:[a-z0-9][a-z0-9._-]{0,99}$/.test(source)
  const validGitHubSource =
    /^[A-Za-z0-9._-]{1,39}\/[A-Za-z0-9._-]{1,100}$/.test(source) &&
    !source.startsWith('.') &&
    !source.startsWith('-') &&
    !['.', '..'].includes(source.split('/')[1])
  const validSource = validGitHubSource || (data?.engine === 'codex' && nativeSource)
  const allowed =
    !!data &&
    (data.any_marketplace || data.allowed_marketplaces.some((s) => s.toLowerCase() === source.toLowerCase()))
  const disabledReason = busy ? 'Saving plugin changes…'
    : source.startsWith('codex:') && data?.engine !== 'codex' ? 'This marketplace requires Codex.'
      : !validSource ? data?.engine === 'codex'
        ? 'Enter a GitHub owner/repo or codex:<marketplace> (1–100 lowercase letters, digits, dots, underscores or hyphens, starting with a letter or digit).'
        : 'Enter a GitHub owner/repo.'
        : !allowed ? 'This marketplace is not allowed by this install.'
          : !validName ? 'Enter a plugin name (1–64 lowercase letters, digits, dots, underscores or hyphens, starting with a letter or digit).'
            : null
  async function add() {
    if (busy || !data?.supported || error || !validName || !validSource || !allowed) return
    setBusy(true)
    setFailure(null)
    try {
      const res = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/plugins`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ marketplace: source, plugin: name, scope: effectiveScope }),
      })
      if (!res.ok) throw new Error('Plugin change was not accepted. Refresh the choices and try again.')
      const p = (await res.json()) as ProposalInfo
      setApplied({ session: sessionId, text: pluginApplication(p), newSession: (p.result as { state?: string } | null)?.state === 'new_sessions' })
      setName('')
      await refresh()
    } catch {
      setFailure('Plugin change was not accepted. Refresh the choices and try again.')
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="session-plugins" aria-label="Session plugins" id={id}>
      <div className="session-plugin-form">
        {error && (
          <p role="alert">
            {error}{' '}
            <button type="button" onClick={() => void refresh()}>
              Retry plugin choices
            </button>
          </p>
        )}
        {!data && !error && <p role="status">Loading plugin choices…</p>}
        {data && !error && (
          <>
            {data.reason && <p>{data.reason}</p>}
            {!data.supported ? (
              <p>Plugins cannot be added to this session.</p>
            ) : (
              <>
                <p>
                  Skills are included in plugin bundles. Choose a marketplace permitted by this install.
                </p>
                <label>
                  Marketplace
                  {data.any_marketplace ? (
                    <input
                      aria-label="Marketplace"
                      value={source}
                      maxLength={140}
                      onChange={(e) => setMarketplace(e.target.value)}
                    />
                  ) : (
                    <select
                      aria-label="Marketplace"
                      value={source}
                      onChange={(e) => setMarketplace(e.target.value)}
                    >
                      {data.allowed_marketplaces.map((s) => (
                        <option key={s} value={s}>
                          {s}
                        </option>
                      ))}
                    </select>
                  )}
                </label>
                <label>
                  Plugin name
                  <input
                    value={name}
                    maxLength={64}
                    onChange={(e) => setName(e.target.value)}
                    autoCapitalize="none"
                    spellCheck={false}
                  />
                </label>
                <label>
                  Apply plugin
                  <select value={effectiveScope} onChange={(e) => setScope(e.target.value as PluginScope)}>
                    <option value="session" disabled={!data.session_apply}>
                      This session only
                    </option>
                    <option value="always">Always on</option>
                  </select>
                </label>
                <p>
                  {effectiveScope === 'session'
                    ? 'Loads between turns; your account settings stay unchanged.'
                    : data.session_apply
                      ? 'Saves to account settings and loads between turns.'
                      : 'Saves to account settings for new sessions. This session keeps its current plugins.'}
                </p>
                <button
                  type="button"
                  disabled={!!disabledReason}
                  aria-describedby={disabledReason ? reasonId : undefined}
                  onClick={() => void add()}
                >
                  {busy ? 'Saving…' : effectiveScope === 'session' ? 'Add to session' : 'Save always on'}
                </button>
                {disabledReason && <p id={reasonId}>{disabledReason}</p>}
              </>
            )}
          </>
        )}
        {failure && <p role="alert">{failure}</p>}
        {applied?.session === sessionId && <p role="status">{applied.text} {applied.newSession && <StartPluginSession onStart={onStart} />}</p>}
      </div>
    </section>
  )
}
export function StartPluginSession({ onStart }: { onStart?: () => void }) {
  return <button type="button" className="btn small" onClick={() => { onStart?.(); openLaunchSheet() }}>Start a new session with it</button>
}

export function PluginRequests({ model, pendingOnly = false }: { model: Model; pendingOnly?: boolean }) {
  const requests = (model.data?.requests ?? []).filter((p) => !pendingOnly || p.state === 'pending')
  if (!requests.length) return null
  return (
    <section className="session-plugins" aria-label="Plugin requests">
      <h3>Plugin requests</h3>
      <ul className="session-plugin-requests">
        {requests.map((p) => (
          <li key={p.id}>
            <strong>{String(p.arguments?.plugin ?? 'Plugin')}</strong>
            {typeof p.arguments?.marketplace === 'string' && <span> from {p.arguments.marketplace}</span>}
            {typeof p.arguments?.reason === 'string' && <p>{p.arguments.reason}</p>}
            <p>{pluginApplication(p)}</p>
            {p.state === 'pending' && (
              <Link to={`/proposals?id=${encodeURIComponent(p.id)}`}>
                Approve or decline {String(p.arguments?.plugin ?? 'plugin')}
              </Link>
            )}
          </li>
        ))}
      </ul>
    </section>
  )
}

export function SessionPluginChatRequests({ sessionId, active }: { sessionId: string; active: boolean }) {
  const model = useSessionPlugins(sessionId, active)
  if (!active) return null
  return (
    <div className="session-plugin-chat-requests">
      <PluginRequests model={model} pendingOnly />
    </div>
  )
}

export function DisallowedPlugins({ model }: { model: Model }) {
  const data = model.data
  if (!data || data.any_marketplace) return null
  const disallowed = data.plugins.filter(p => !data.allowed_marketplaces.some(s => s.toLowerCase() === p.marketplace.toLowerCase()))
  if (!disallowed.length) return null
  return <section className="session-plugins" aria-label="Disallowed saved plugins"><ul>{disallowed.map(p => <li key={`${p.marketplace}/${p.plugin}`}><strong>{p.plugin}</strong>: marketplace no longer allowed ({p.marketplace}). Kept in your account; it will not load in new sessions.</li>)}</ul></section>
}
