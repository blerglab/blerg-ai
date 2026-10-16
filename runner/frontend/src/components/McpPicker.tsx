// McpPicker: choose which of the person's MCP connections a session may use, and for each
// tool whether it is off, queued for approval, or allowed (spec 6.2, 6.3).
//
// Everything starts off: no connection checked, every tool off. A checked connection offers
// three presets — Allow all, Require approval (read-only tools allowed, the rest proposed),
// None — and Customize, which opens the per-tool list. A server's readOnlyHint is only a
// badge (the server controls it) and is what the presets go by; it is never pre-selected.
// `allow` on a tool not marked read-only carries a warning. `propose` queues the call for the
// person's approval on the Proposals page.
//
// The value it emits is the launch payload: [{connection, tools: {name: {mode, hash}}}] with
// only tools that are not off, and only connections that have at least one. Reusable by the
// crons form: value/onChange are the whole interface; requireExplicit drops the presets so
// each tool must be chosen by hand.
import { useCallback, useEffect, useRef, useState } from 'react'
import { apiFetch } from '../apiFetch'
import type { McpConnectionInfo, McpSelectionEntry, McpToolInfo, McpToolMode } from '../types'
import {
  MCP_ALLOW_WARNING, MCP_PRESET_LABEL, MCP_PROPOSE_LABEL, buildSelection, isReadOnlyTool, presetModes, presetOf,
  type McpModes, type McpPreset, type McpPresetClaims, type McpSeenHashes, type McpToolsState,
} from '../lib/mcpSelection'
import { escapeHidden, isPlainName } from '../lib/toolChanges'

// Where the connection list comes from: the last list, if one is kept, shown
// at once, and a fresh one. Tool listings are never part of it.
export interface McpConnectionsSource {
  cached: () => McpConnectionInfo[] | undefined
  load: () => Promise<McpConnectionInfo[]>
}

interface Props {
  value: McpSelectionEntry[]
  onChange: (next: McpSelectionEntry[]) => void
  // No presets: every tool's mode must be picked by hand (the crons form).
  requireExplicit?: boolean
  // A stable source (the launch sheet's cache); without one the list is asked
  // for on every mount.
  connections?: McpConnectionsSource
}

const liveConnections: McpConnectionsSource = {
  cached: () => undefined,
  load: async () => {
    const res = await apiFetch('/api/mcp/connections')
    if (!res.ok) throw new Error(await errorText(res))
    const body = await res.json() as { connections?: McpConnectionInfo[] }
    return body.connections ?? []
  },
}

async function errorText(res: Response): Promise<string> {
  try {
    const body = await res.json() as { error?: string }
    if (body.error) return body.error
  } catch { /* fall through */ }
  return `HTTP ${res.status}`
}

function notReadyReason(c: McpConnectionInfo): string {
  return c.status === 'needs_auth'
    ? 'the connection needs to be signed in again; fix it in the connection settings'
    : `the connection is not ready (status: ${c.status}); fix it in the connection settings`
}

const box: React.CSSProperties = { border: '1px solid var(--stone)', borderRadius: 8, background: 'var(--scree)', padding: '8px 12px' }
const muted: React.CSSProperties = { color: 'var(--fog)', fontSize: 13 }
// The runner's button pair (as in Proposals and the crons page).
const button: React.CSSProperties = {
  background: 'var(--scree)', border: '1px solid var(--stone)', borderRadius: 6, color: 'var(--fog)',
  padding: '5px 12px', cursor: 'pointer', fontSize: '0.85rem', fontFamily: 'inherit',
}
const primary: React.CSSProperties = {
  ...button, background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))', border: '1px solid var(--blaze)', color: 'var(--amber)', fontWeight: 700,
}

// The first 200 code points (not UTF-16 units, so a surrogate pair is never split), with an ellipsis when cut.
function descriptionCut(d: string): string {
  const chars = Array.from(d)
  return chars.length > 200 ? chars.slice(0, 200).join('') + '…' : d
}

export default function McpPicker({ value, onChange, requireExplicit = false, connections = liveConnections }: Props) {
  const [conns, setConns] = useState<McpConnectionInfo[] | null>(() => connections.cached() ?? null)
  const [connError, setConnError] = useState<string | null>(null)
  // What came in as value: the hashes it named, its connections checked, its modes set.
  const [seen] = useState<McpSeenHashes>(() => {
    const s: McpSeenHashes = {}
    for (const v of value) s[v.connection] = Object.fromEntries(Object.entries(v.tools).map(([n, t]) => [n, t.hash]))
    return s
  })
  const [checked, setChecked] = useState<Set<string>>(() => new Set(value.map(v => v.connection)))
  const [modes, setModes] = useState<McpModes>(() => {
    const m: McpModes = {}
    for (const v of value) m[v.connection] = Object.fromEntries(Object.entries(v.tools).map(([n, t]) => [n, t.mode]))
    return m
  })
  const [tools, setTools] = useState<Record<string, McpToolsState>>({})
  // The preset last clicked per connection, until a per-tool edit follows.
  const [claims, setClaims] = useState<McpPresetClaims>({})
  // Connections whose per-tool list is open (Customize).
  const [custom, setCustom] = useState<Set<string>>(() => new Set())

  // The async tool load outlives the render that started it: it reads the latest state and the
  // latest onChange from here (written in an effect, never during render).
  const latest = useRef({ checked, modes, tools, claims, onChange })
  useEffect(() => { latest.current = { checked, modes, tools, claims, onChange } })
  const alive = useRef(true)
  useEffect(() => {
    alive.current = true
    return () => { alive.current = false }
  }, [])

  const emit = useCallback((c: Set<string>, m: McpModes, t: Record<string, McpToolsState>, cl: McpPresetClaims) => {
    latest.current.onChange(buildSelection(c, m, t, seen, cl))
  }, [seen])

  const loadTools = useCallback((id: string) => {
    setTools(t => ({ ...t, [id]: { status: 'loading' } }))
    apiFetch(`/api/mcp/connections/${encodeURIComponent(id)}/tools`)
      .then(async res => {
        if (!res.ok) throw new Error(await errorText(res))
        const body = await res.json() as { tools?: McpToolInfo[] }
        return body.tools ?? []
      })
      .then(list => {
        if (!alive.current) return
        const { checked: nowChecked, modes: nowModes, tools: nowTools } = latest.current
        // A tool that changed since the value was made stays off until it is chosen again.
        const kept: Record<string, McpToolMode> = {}
        for (const [name, mode] of Object.entries(nowModes[id] ?? {})) {
          const live = list.find(t => t.name === name)
          const was = seen[id]?.[name]
          if (live && (was === undefined || was === live.hash)) kept[name] = mode
        }
        const nextModes = { ...nowModes, [id]: kept }
        const nextTools: Record<string, McpToolsState> = { ...nowTools, [id]: { status: 'ready', tools: list } }
        setModes(nextModes)
        setTools(t => ({ ...t, [id]: { status: 'ready', tools: list } }))
        if (Object.keys(nowModes[id] ?? {}).length > 0) emit(nowChecked, nextModes, nextTools, latest.current.claims)
      })
      .catch((e: unknown) => {
        if (!alive.current) return
        setTools(t => ({ ...t, [id]: { status: 'error', message: e instanceof Error ? e.message : String(e) } }))
      })
  }, [seen, emit])

  // Asked again on every mount, even with a cached list showing. Tools are
  // only ever loaded from this live answer (or a later click), never from the
  // cached list.
  useEffect(() => {
    let cancelled = false
    const hadCached = connections.cached() !== undefined
    connections.load()
      .then(list => {
        if (cancelled) return
        setConns(list)
        // A value that came in with connections already chosen (an edit) shows their tools —
        // unless they are already loading or loaded (checked from the cached list meanwhile).
        for (const c of list) {
          if (c.status === 'ok' && latest.current.checked.has(c.id) && !latest.current.tools[c.id]) loadTools(c.id)
        }
      })
      .catch((e: unknown) => {
        // A failed refresh under a cached list keeps the list; with none, say so.
        if (!cancelled && !hadCached) setConnError(e instanceof Error ? e.message : String(e))
      })
    return () => { cancelled = true }
  }, [loadTools, connections])

  function toggle(id: string, on: boolean) {
    const next = new Set(checked)
    if (on) next.add(id)
    else next.delete(id)
    setChecked(next)
    if (on && tools[id]?.status !== 'ready' && tools[id]?.status !== 'loading') loadTools(id)
    emit(next, modes, tools, claims)
  }

  function setMode(id: string, name: string, mode: McpToolMode) {
    const nextModes = { ...modes, [id]: { ...modes[id], [name]: mode } }
    const nextClaims = { ...claims, [id]: undefined }
    setModes(nextModes)
    setClaims(nextClaims)
    emit(checked, nextModes, tools, nextClaims)
  }

  function applyPreset(id: string, preset: McpPreset) {
    const st = tools[id]
    if (st?.status !== 'ready') return
    const nextModes = { ...modes, [id]: presetModes(preset, st.tools) }
    const nextClaims = { ...claims, [id]: requireExplicit || preset === 'none' ? undefined : preset }
    setModes(nextModes)
    setClaims(nextClaims)
    emit(checked, nextModes, tools, nextClaims)
  }

  if (connError) return <div data-testid="mcp-error" style={{ ...muted, color: 'var(--danger)' }}>Could not load MCP connections: {connError}</div>
  if (conns === null) return <div style={muted}>Loading MCP connections...</div>
  // Chosen connections core no longer lists (deleted, or not the person's): shown so they can be
  // removed. Left in the value they would be refused whenever the selection is changed.
  const orphans = [...checked].filter(id => !conns.some(c => c.id === id))
  if (conns.length === 0 && orphans.length === 0) return <div data-testid="mcp-none" style={muted}>No MCP connections. Add one in the connection settings.</div>

  function removeOrphan(id: string) {
    const next = new Set(checked)
    next.delete(id)
    const nextModes = { ...modes }
    delete nextModes[id]
    const nextClaims = { ...claims, [id]: undefined }
    setChecked(next)
    setModes(nextModes)
    setClaims(nextClaims)
    emit(next, nextModes, tools, nextClaims)
  }

  return (
    <div data-testid="mcp-picker" style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      {orphans.map(id => (
        <div key={id} data-testid={`mcp-orphan-${id}`} style={{ ...box, borderColor: 'var(--danger)' }}>
          <div style={{ color: 'var(--chalk)', fontWeight: 600 }}>Missing connection</div>
          <div style={{ ...muted, color: 'var(--danger)' }}>
            Connection {id} no longer exists (it was deleted, or is not yours), so its tools cannot be used.
          </div>
          <button type="button" style={button} onClick={() => removeOrphan(id)} aria-label={`Remove missing connection ${id}`}>Remove</button>
        </div>
      ))}
      {conns.map(c => {
        const usable = c.status === 'ok'
        const chosen = checked.has(c.id)
        const on = chosen && usable
        const st = tools[c.id]
        const modeOf = (name: string): McpToolMode => modes[c.id]?.[name] ?? 'off'
        const anyOn = Object.values(modes[c.id] ?? {}).some(m => m !== 'off')
        const preset = st?.status === 'ready' ? presetOf(modes[c.id], st.tools) : 'none'
        const showList = requireExplicit || custom.has(c.id) || preset === 'custom'
        const acting = st?.status === 'ready' ? st.tools.filter(t => !isReadOnlyTool(t)).length : 0
        return (
          <div key={c.id} data-testid={`mcp-conn-${c.name}`} style={{ ...box, opacity: usable ? 1 : 0.6 }}>
            <label style={{ display: 'flex', gap: 8, alignItems: 'center', cursor: usable || chosen ? 'pointer' : 'not-allowed' }}>
              <input
                type="checkbox"
                checked={chosen}
                disabled={!usable && !chosen}
                onChange={e => toggle(c.id, e.target.checked)}
                aria-label={c.name}
              />
              <span style={{ color: 'var(--chalk)', fontWeight: 600 }}>{c.name}</span>
            </label>
            {!usable && <div data-testid={`mcp-reason-${c.name}`} style={muted}>Unavailable: {notReadyReason(c)}</div>}
            {on && st?.status === 'loading' && <div style={muted}>Loading tools...</div>}
            {on && st?.status === 'error' && (
              <div data-testid={`mcp-tools-error-${c.name}`} style={{ ...muted, color: 'var(--danger)' }}>
                Could not load tools: {st.message}{' '}
                <button type="button" style={button} onClick={() => loadTools(c.id)}>Retry</button>
              </div>
            )}
            {on && st?.status === 'ready' && (
              <div style={{ marginTop: 8 }}>
                {st.tools.length === 0 && <div style={muted}>This server lists no tools.</div>}
                {!requireExplicit && st.tools.length > 0 && (
                  <div data-testid={`mcp-presets-${c.name}`} style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
                    {(['allow_all', 'approval', 'none'] as McpPreset[]).map(p => (
                      <button
                        key={p}
                        type="button"
                        data-testid={`mcp-preset-${c.name}-${p}`}
                        aria-pressed={preset === p}
                        onClick={() => applyPreset(c.id, p)}
                        style={preset === p ? primary : button}
                      >
                        {MCP_PRESET_LABEL[p]}
                      </button>
                    ))}
                    {preset === 'custom' && <span data-testid={`mcp-preset-${c.name}-custom`} style={{ ...muted, fontWeight: 700 }}>Custom</span>}
                    <button
                      type="button"
                      data-testid={`mcp-customize-${c.name}`}
                      aria-expanded={showList}
                      onClick={() => setCustom(s => { const n = new Set(s); if (n.has(c.id)) n.delete(c.id); else n.add(c.id); return n })}
                      style={{ ...button, marginLeft: 'auto' }}
                    >
                      {showList ? 'Hide tools' : 'Customize'}
                    </button>
                  </div>
                )}
                {!requireExplicit && preset === 'allow_all' && acting > 0 && (
                  <div role="alert" data-testid={`mcp-warn-${c.name}`} style={{ color: 'var(--danger)', fontSize: 12, marginTop: 4 }}>
                    {acting} of {st.tools.length} tools can change things: {MCP_ALLOW_WARNING}.
                  </div>
                )}
                {!requireExplicit && preset === 'approval' && (
                  <div style={{ ...muted, marginTop: 4 }}>Read-only tools allowed; the other {acting} {MCP_PROPOSE_LABEL}.</div>
                )}
                {showList && <div style={{ maxHeight: 260, overflowY: 'auto', marginTop: 6, display: 'flex', flexDirection: 'column', gap: 6 }}>
                  {st.tools.map(t => {
                    const mode = t.unsafe ? 'off' : modeOf(t.name)
                    const ro = isReadOnlyTool(t)
                    return (
                      <div key={t.name} data-testid={`mcp-tool-${c.name}-${t.name}`}>
                        <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
                          <span style={{ color: 'var(--chalk)', fontFamily: 'monospace', fontSize: 13 }}>{escapeHidden(t.name).text}</span>
                          {!isPlainName(t.name) && <span style={{ ...muted, color: 'var(--danger)', fontSize: 11 }}>name has unusual characters</span>}
                          {ro && <span style={{ ...muted, border: '1px solid var(--stone)', borderRadius: 4, padding: '0 4px', fontSize: 11 }}>read-only (claimed by the server)</span>}
                          <select
                            aria-label={`${c.name} ${t.name} mode`}
                            value={mode}
                            disabled={!!t.unsafe}
                            onChange={e => setMode(c.id, t.name, e.target.value as McpToolMode)}
                            style={{ marginLeft: 'auto' }}
                          >
                            <option value="off">off</option>
                            <option value="propose" title={MCP_PROPOSE_LABEL}>propose ({MCP_PROPOSE_LABEL})</option>
                            <option value="allow">allow</option>
                          </select>
                        </div>
                        {t.description && (
                          <div style={{ ...muted, fontSize: 12 }}>
                            {escapeHidden(descriptionCut(t.description)).text}
                            {escapeHidden(t.description).hidden && <span style={{ ...muted, color: 'var(--danger)', fontSize: 11 }}> contains hidden characters</span>}
                          </div>
                        )}
                        {t.unsafe && <div style={{ ...muted, color: 'var(--danger)', fontSize: 12 }}>cannot be reviewed (hidden characters or too large); off</div>}
                        {mode === 'allow' && !ro && (requireExplicit || preset !== 'allow_all') && (
                          <div role="alert" data-testid={`mcp-warn-${c.name}-${t.name}`} style={{ color: 'var(--danger)', fontSize: 12 }}>
                            Warning: {MCP_ALLOW_WARNING}.
                          </div>
                        )}
                      </div>
                    )
                  })}
                </div>}
                {!anyOn && <div style={{ ...muted, marginTop: 6 }}>No tools selected: this server will not be attached.</div>}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}
