// The plugin browser's data (card #144): the marketplaces the session's engine knows and their
// plugins, read by the session's daemon (GET /api/sessions/{id}/plugins/catalog). Everything in it
// is rendered as plain text. An engine with no catalog answers 409 "unsupported" (or "sandboxed"):
// the browser then shows that engine's add form instead.
import { apiFetch } from '../apiFetch'
import type { SessionPluginSettings } from './sessionPlugins'

export interface CatalogMarketplace {
  name: string
  /** GitHub owner/repo the marketplace comes from; absent when it is not one Blerg installs from. */
  source?: string
  description?: string
}

export interface CatalogPlugin {
  name: string
  marketplace: string
  description?: string
  version?: string
  author?: string
  category?: string
  installed?: boolean
  enabled?: boolean
}

export interface PluginCatalog {
  engine: string
  marketplaces: CatalogMarketplace[]
  plugins: CatalogPlugin[]
  truncated: boolean
}

export type CatalogResult =
  | { kind: 'ok'; catalog: PluginCatalog }
  | { kind: 'fallback'; code: string }
  | { kind: 'error'; message: string }

const str = (v: unknown, max: number): string | undefined => (typeof v === 'string' && v ? v.slice(0, max) : undefined)

/** Re-validates a catalog body: anything malformed is dropped, never rendered. */
export function parseCatalog(body: unknown): PluginCatalog | null {
  if (!body || typeof body !== 'object') return null
  const b = body as Record<string, unknown>
  if (!Array.isArray(b.marketplaces) || !Array.isArray(b.plugins)) return null
  const marketplaces: CatalogMarketplace[] = []
  const names = new Set<string>()
  for (const m of b.marketplaces.slice(0, 64)) {
    const name = str((m as Record<string, unknown>)?.name, 100)
    if (!name || names.has(name)) continue
    names.add(name)
    const r = m as Record<string, unknown>
    marketplaces.push({ name, source: str(r.source, 141), description: str(r.description, 500) })
  }
  const plugins: CatalogPlugin[] = []
  for (const p of b.plugins.slice(0, 4000)) {
    const r = (p ?? {}) as Record<string, unknown>
    const name = str(r.name, 64)
    const marketplace = str(r.marketplace, 100)
    if (!name || !marketplace || !names.has(marketplace)) continue
    plugins.push({
      name, marketplace,
      description: str(r.description, 500), version: str(r.version, 100), author: str(r.author, 100), category: str(r.category, 100),
      installed: r.installed === true, enabled: r.enabled === true,
    })
  }
  return { engine: typeof b.engine === 'string' ? b.engine : '', marketplaces, plugins, truncated: b.truncated === true }
}

async function catalogReply(res: Response): Promise<CatalogResult> {
  const body: unknown = await res.json().catch(() => null)
  if (res.ok) {
    const catalog = parseCatalog(body)
    return catalog ? { kind: 'ok', catalog } : { kind: 'error', message: 'The catalog could not be read.' }
  }
  const code = typeof (body as { code?: unknown })?.code === 'string' ? (body as { code: string }).code : ''
  if (code === 'unsupported' || code === 'sandboxed') return { kind: 'fallback', code }
  const message = typeof (body as { error?: unknown })?.error === 'string' ? (body as { error: string }).error : 'The catalog could not be read.'
  return { kind: 'error', message }
}

export async function fetchCatalog(sessionId: string): Promise<CatalogResult> {
  try {
    return await catalogReply(await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/plugins/catalog`))
  } catch {
    return { kind: 'error', message: 'The catalog could not be read.' }
  }
}

export async function addMarketplace(sessionId: string, source: string): Promise<CatalogResult> {
  try {
    return await catalogReply(await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/plugins/marketplaces`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ source }),
    }))
  } catch {
    return { kind: 'error', message: 'The marketplace could not be added.' }
  }
}

const OWNER_REPO = /^[A-Za-z0-9._-]{1,39}\/[A-Za-z0-9._-]{1,100}$/

/** What a person may type as a marketplace: owner/repo or an https://github.com/owner/repo URL. */
export function validMarketplaceInput(raw: string): boolean {
  const s = raw.trim()
  if (OWNER_REPO.test(s)) return !s.startsWith('.') && !s.startsWith('-')
  try {
    const u = new URL(s)
    if (u.protocol !== 'https:' || u.username || u.password || u.search || u.hash) return false
    if (!/^(www\.)?github\.com$/i.test(u.hostname)) return false
    const path = u.pathname.replace(/^\/|\/$/g, '').replace(/\.git$/, '')
    return OWNER_REPO.test(path)
  } catch {
    return false
  }
}

/** Why a plugin cannot be installed from here, or null when it can. */
export function installBlocker(market: CatalogMarketplace | undefined, settings: SessionPluginSettings | null): string | null {
  if (!settings || !settings.supported) return 'Plugins cannot be added to this session.'
  if (!market?.source) return 'Installed from its own marketplace only: not a GitHub marketplace.'
  const src = market.source.toLowerCase()
  if (!settings.any_marketplace && !settings.allowed_marketplaces.some(s => s.toLowerCase() === src)) return 'This marketplace is not allowed by this install.'
  return null
}

/** Whether Blerg already has this plugin always on for the account. */
export function alwaysOn(market: CatalogMarketplace | undefined, plugin: string, settings: SessionPluginSettings | null): boolean {
  const src = market?.source?.toLowerCase()
  return !!src && !!settings?.plugins.some(p => p.plugin === plugin && p.marketplace.toLowerCase() === src)
}

export function matchesPlugin(p: CatalogPlugin, q: string): boolean {
  if (!q) return true
  const needle = q.toLowerCase()
  return [p.name, p.description, p.marketplace, p.author, p.category].some(v => v?.toLowerCase().includes(needle))
}

// ── MCP sign-in ──────────────────────────────────────────────────────────────

/** Where claude.ai connectors are connected: the page Claude Code itself sends people to. */
export const CLAUDE_AI_CONNECTORS_URL = 'https://claude.ai/customize/connectors'

export type SignInResult =
  | { kind: 'url'; url: string; callback: boolean }
  | { kind: 'done' }
  | { kind: 'error'; message: string; code?: string }

/** Only an absolute https URL with a host and no credentials is ever opened. */
export function safeAuthURL(raw: unknown): string | null {
  if (typeof raw !== 'string' || raw.length > 4096) return null
  try {
    const u = new URL(raw)
    if (u.protocol !== 'https:' || !u.hostname || u.username || u.password) return null
    return u.href
  } catch {
    return null
  }
}

export async function startMcpSignIn(sessionId: string, server: string): Promise<SignInResult> {
  try {
    const res = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/mcp/sign-in`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ server }),
    })
    const body = (await res.json().catch(() => null)) as Record<string, unknown> | null
    if (!res.ok) {
      const message = typeof body?.error === 'string' ? body.error : 'The sign-in could not be started.'
      return { kind: 'error', message, code: typeof body?.code === 'string' ? body.code : undefined }
    }
    if (body?.requires_user_action !== true) return { kind: 'done' }
    const url = safeAuthURL(body?.auth_url)
    if (!url) return { kind: 'error', message: 'The engine did not return a usable sign-in link.' }
    return { kind: 'url', url, callback: body?.callback_expected === true }
  } catch {
    return { kind: 'error', message: 'The sign-in could not be started.' }
  }
}
