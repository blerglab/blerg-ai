// launchCache: what the New session sheet last knew, so it opens at once.
//
// The sheet needs four answers before it can say much — the repositories, the
// connected daemons, the cluster's status and which personal credentials the
// person has — plus, once a runtime is chosen, a model list and the MCP
// connections. Asked from scratch on every open, the slowest of them (the
// repository list, which can wait on GitHub) decided how long the sheet said
// "Loading...". This module keeps the last answer of each, per part and with
// the time it arrived, so the sheet can show them at once and refresh
// underneath (stale-while-revalidate).
//
// Kept in this tab only: module memory, mirrored to sessionStorage (never
// localStorage — repository names can be private) under the signed-in
// account, so another account in the same tab never sees it. A 401 (signed
// out) drops it. MCP tool listings are deliberately NOT here: a session's tool
// pins must come from a listing taken in the sheet that launches it.
import { apiFetch } from '../apiFetch'
import { getAccessToken } from '../authClient'
import { tokenExpired } from '../claims'
import type { McpConnectionInfo } from '../types'
import { engineKey, type FetchedModelList, type ModelListCache, type ModelRuntime } from './engineModels'

export interface RepoInfo {
  name: string
  full_name: string
  checked_out: string[]
  is_local?: boolean
  // The git provider full_name lives on ("github", "gitlab", …). Set on every
  // hosted entry and on a local folder whose origin is known; absent only on
  // an unresolved local folder. Sent back with a cluster launch so the pod
  // clones from the right host.
  provider?: string
  // Local-only folders: the owner/name the daemon read from the folder's
  // origin remote (the folder's name need not match it). full_name is set to
  // it when known; cloneable === false means full_name is only the bare
  // folder name, which a cluster pod cannot clone.
  remote?: string
  cloneable?: boolean
}

export interface DaemonApiInfo {
  ownership_revision?: number
  connection_generation?: string
  discovery_version?: number
  workspace_capabilities?: { manifest_version: number; shell_version: number }

  id: string
  name: string
  mode: string
  repos_root: string
  status: string
  sandbox_available: boolean
  available_engines: string[]
  codex_restricted?: boolean
  // The daemon dials the MCP gateway through the server's web address, so a
  // session on its host (This machine) can take a board. Absent from older daemons.
  mcp_gateway_via_server?: boolean
  plugin_engines?: string[]
  // What the daemon can run a host Claude agent session on: its `claude`
  // CLI (the user's login) or, failing that, an API key. Absent from older
  // daemons — then nothing is claimed either way.
  claude_cli_available?: boolean
  anthropic_key_set?: boolean
  // A Local sandbox Claude session would find a credential on this daemon:
  // a host ~/.claude login to mount, or CLAUDE_CODE_OAUTH_TOKEN /
  // ANTHROPIC_API_KEY in the daemon's environment. false = such a launch is
  // refused. Absent from older daemons — then nothing is claimed either way.
  sandbox_claude_credential?: boolean
  // The daemon can clone a repository named by owner/name or URL into its
  // repos folder. Absent/false for an older daemon (or server): it is then
  // offered only its folders and the listed repositories.
  clone_from?: boolean
  // The daemon's owner lets a This machine session clone a private
  // repository with the person's own token. Otherwise only Local sandbox
  // does: on the bare host another session could read the token mid-clone.
  allow_host_credential_clone?: boolean
}

export interface ClusterStatusApi {
  configured: boolean
  /** BLERG_RUNNER_SANDBOX_CLUSTER_CHILDREN: "once" (a sandboxed desktop session asks for each
   *  cluster child) or "any" (it may have them preapproved). Absent means "once". */
  sandbox_cluster_children?: 'once' | 'any'
  available_engines?: string[]
  // Whether the operator's Secret carries a shared git token. A user with
  // their own GitHub credential doesn't need it; a user without one can only
  // clone public repositories when it is absent.
  git_configured?: boolean
}

// MyCredentialsApi mirrors GET /api/me/credentials: which personal credentials
// the signed-in account has in blerg-core, presence only. `engines` never
// includes a git provider's kind ("github", "gitlab") — those are reported
// separately as `git` (any) and `git_providers` (which), because they gate
// cloning rather than engine choice. A response with `unavailable: true`
// (core unreachable/unconfigured), or a failed fetch, is stored as null =
// "unknown", which suppresses every hard block in the sheet.
export interface MyCredentialsApi {
  engines: string[]
  git: boolean
  git_providers?: string[]
  unavailable?: boolean
}

// GET /api/repos, as the sheet uses it: the list, and whether a provider's
// list failed to refresh (stale_at).
export interface ReposPart {
  repos: RepoInfo[]
  stale: boolean
  // The server's ETag for this exact list: the next ask sends it as
  // If-None-Match, and a 304 keeps this list. The browser itself keeps no
  // copy (the server says no-store) — this tab's cache is the only one.
  etag?: string
}

// An MCP connection as kept: what the picker shows, nothing more — no URL,
// and no default tool modes or hashes (pins come from a live listing only).
type CachedMcpConnection = Pick<McpConnectionInfo, 'id' | 'name' | 'auth_kind' | 'status'>

export interface Stamped<T> {
  value: T
  at: number // launchClock.now() when it arrived
}

export interface LaunchCacheData {
  repos?: Stamped<ReposPart>
  daemons?: Stamped<DaemonApiInfo[]>
  cluster?: Stamped<ClusterStatusApi>
  creds?: Stamped<MyCredentialsApi | null>
  mcpConnections?: Stamped<CachedMcpConnection[]>
  // "<engine>\n<daemon id or ''>" → that list.
  modelsByKey: Record<string, Stamped<FetchedModelList>>
}

// An answer younger than this is as good as asking again: opening the sheet
// right after a prefetch (hover, then click) asks nothing more.
export const FRESH_MS = 10_000

const STORAGE_KEY = 'blerg.launch.cache'

interface Stored {
  account: string
  data: LaunchCacheData
}

let cacheGeneration = 0
let mem: Stored | null = null
const inflight = new Map<string, Promise<unknown>>()

// The signed-in account, read (unverified) from the access token's sub — only
// to keep one account's cached lists from another's in this tab; the server
// still decides what each request may see. '' when there is no token.
function currentAccount(): string {
  const token = getAccessToken()
  if (!token) return ''
  try {
    const payload = JSON.parse(atob(token.split('.')[1].replace(/-/g, '+').replace(/_/g, '/'))) as { sub?: unknown }
    return typeof payload.sub === 'string' ? payload.sub : ''
  } catch {
    return ''
  }
}

function empty(): LaunchCacheData {
  return { modelsByKey: {} }
}

function load(account: string): LaunchCacheData {
  if (mem && mem.account === account) return mem.data
  let data = empty()
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY)
    const stored = raw ? (JSON.parse(raw) as Stored) : null
    if (stored && stored.account === account && stored.data && typeof stored.data === 'object') {
      data = { ...stored.data, modelsByKey: stored.data.modelsByKey ?? {} }
    } else if (stored) {
      sessionStorage.removeItem(STORAGE_KEY) // another account's
    }
  } catch {
    // Unreadable or no storage: start empty.
  }
  mem = { account, data }
  return data
}

function save(update: (d: LaunchCacheData) => LaunchCacheData) {
  const account = currentAccount()
  const data = update(load(account))
  mem = { account, data }
  try {
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify(mem))
  } catch {
    // Storage full or unavailable: memory still has it.
  }
}

// readLaunchCache is what the sheet last knew, for the signed-in account.
export function readLaunchCache(): LaunchCacheData {
  return load(currentAccount())
}

// The clock every stamp is read from (tests move it).
export const launchClock = { now: () => Date.now() }

export function isFresh(part: Stamped<unknown> | undefined, now = launchClock.now()): boolean {
  return !!part && now - part.at < FRESH_MS
}

// clearLaunchCache forgets everything (signed out; and between tests).
export function clearLaunchCache() {
  cacheGeneration++
  reposGen++
  mem = null
  inflight.clear()
  try {
    sessionStorage.removeItem(STORAGE_KEY)
  } catch {
    // nothing to clear
  }
}

// One request per part at a time: a prefetch and the sheet opening share it.
function once<T>(key: string, run: (saveResult: typeof save) => Promise<T>): Promise<T> {
  const account = currentAccount()
  let generation = cacheGeneration
  const scopedKey = `${account}:${generation}:${key}`
  const running = inflight.get(scopedKey) as Promise<T> | undefined
  if (running) return running
  const check = () => {
    if (account !== currentAccount() || generation !== cacheGeneration) throw new SignedOut()
  }
  const guardedSave: typeof save = update => { check(); save(update); generation = cacheGeneration }
  const p = run(guardedSave).then(value => { check(); return value }).finally(() => {
    if (inflight.get(scopedKey) === p) inflight.delete(scopedKey)
  })
  inflight.set(scopedKey, p)
  return p
}

class SignedOut extends Error {
  constructor() { super('signed out') }
}

// checkSignedIn drops everything on a 401 that apiFetch's own renewal could
// not cure: the person is signed out.
function checkSignedIn(res: Response) {
  if (res.status === 401) {
    clearLaunchCache()
    throw new SignedOut()
  }
}

async function getJSON(url: string): Promise<unknown> {
  const res = await apiFetch(url)
  checkSignedIn(res)
  if (res.ok === false) throw new Error(`HTTP ${res.status}`)
  return res.json()
}

// reposPartFrom reads a 200 answer of GET /api/repos.
export async function reposPartFrom(res: Response): Promise<ReposPart> {
  const data = (await res.json()) as { repos?: RepoInfo[]; stale_at?: string | null }
  return { repos: data?.repos ?? [], stale: !!data?.stale_at, etag: res.headers?.get('ETag') ?? undefined }
}

// The repository list, asked conditionally: with a kept list the request
// names its ETag, and a 304 keeps that list (now as fresh as a full answer).
export function fetchRepos(): Promise<ReposPart> {
  return once('repos', async (save) => {
    const gen = reposGen
    const kept = readLaunchCache().repos?.value
    const res = await apiFetch('/api/repos', kept?.etag ? { headers: { 'If-None-Match': kept.etag } } : undefined)
    checkSignedIn(res)
    const value = res.status === 304 && kept ? kept : res.ok === false ? null : await reposPartFrom(res)
    if (!value) throw new Error(`HTTP ${res.status}`)
    // A list stored meanwhile (the repos folder changed) is newer: keep it.
    if (gen === reposGen) save(d => ({ ...d, repos: { value, at: launchClock.now() } }))
    return value
  })
}

export function fetchDaemons(): Promise<DaemonApiInfo[]> {
  return once('daemons', async (save) => {
    const data = await getJSON('/api/daemons')
    const value = Array.isArray(data) ? (data as DaemonApiInfo[]) : []
    save(d => {
      if (d.daemons && daemonScope(d.daemons.value) !== daemonScope(value)) {
        cacheGeneration++; reposGen++; inflight.clear()
        return { ...empty(), daemons: { value, at: launchClock.now() } }
      }
      return { ...d, daemons: { value, at: launchClock.now() } }
    })
    return value
  })
}

export function fetchCluster(): Promise<ClusterStatusApi> {
  return once('cluster', async (save) => {
    const value = ((await getJSON('/api/cluster/status')) ?? { configured: false }) as ClusterStatusApi
    save(d => ({ ...d, cluster: { value, at: launchClock.now() } }))
    return value
  })
}

// Personal credentials are advisory: a failure here never fails the sheet,
// it resolves to null ("unknown") instead.
export function fetchCreds(): Promise<MyCredentialsApi | null> {
  return once('creds', async (save) => {
    let value: MyCredentialsApi | null = null
    try {
      const c = (await getJSON('/api/me/credentials')) as MyCredentialsApi
      value = c && !c.unavailable ? c : null
    } catch (err) {
      // Signed out: the cache was just dropped; keep nothing, not even "unknown".
      if (err instanceof SignedOut) return null
      value = null
    }
    save(d => ({ ...d, creds: { value, at: launchClock.now() } }))
    return value
  }).catch(err => { if (err instanceof SignedOut) return null; throw err })
}

async function errorText(res: Response): Promise<string> {
  try {
    const body = await res.json() as { error?: string }
    if (body.error) return body.error
  } catch { /* fall through */ }
  return `HTTP ${res.status}`
}

// The person's MCP connections. The live answer goes to the picker whole;
// what is kept is stripped to id, name, auth kind and status — the answer's
// default tool modes carry tool hashes, and those (like every tool listing)
// are never kept: a session's pins come from a listing taken in the sheet.
export function fetchMcpConnections(): Promise<McpConnectionInfo[]> {
  return once('mcpConnections', async (save) => {
    const res = await apiFetch('/api/mcp/connections')
    checkSignedIn(res)
    if (!res.ok) throw new Error(await errorText(res))
    const body = await res.json() as { connections?: McpConnectionInfo[] }
    const value = body.connections ?? []
    const kept: CachedMcpConnection[] = value.map(({ id, name, auth_kind, status }) => ({ id, name, auth_kind, status }))
    save(d => ({ ...d, mcpConnections: { value: kept, at: launchClock.now() } }))
    return value
  })
}

// McpPicker's source of connections in the sheet: the kept list at once (with
// no URL and no default tools), and the refreshed one.
export const launchMcpConnections = {
  cached: (): McpConnectionInfo[] | undefined =>
    readLaunchCache().mcpConnections?.value.map(({ id, name, auth_kind, status }) => ({ id, name, auth_kind, status, url: '', default_tools: {} })),
  load: fetchMcpConnections,
}

function daemonScope(daemons: DaemonApiInfo[]): string {
  return daemons.map(d => `${d.id}:${d.ownership_revision ?? ''}:${d.connection_generation ?? ''}`).sort().join('|')
}
function modelDesktopScope(daemonId?: string): string {
  const daemons = readLaunchCache().daemons?.value ?? []
  return daemonScope(daemonId ? daemons.filter(d => d.id === daemonId) : daemons)
}

function modelsKey(engine: string, daemonId?: string, runtime?: ModelRuntime): string {
  return `${engineKey(engine)}\n${daemonId ?? ''}${runtime ? `\n${runtime}` : ''}${modelDesktopScope(daemonId) ? `\n${modelDesktopScope(daemonId)}` : ''}`
}

// The sheet's model lists, per engine and daemon: useEngineModels shows the
// cached one at once and puts each answer back.
export const launchModelsCache: ModelListCache = {
  identity: () => `${currentAccount()}:${cacheGeneration}:${modelDesktopScope()}`,
  get: (engine: string, daemonId?: string, runtime?: ModelRuntime): FetchedModelList | undefined =>
    readLaunchCache().modelsByKey[modelsKey(engine, daemonId, runtime)]?.value,
  put: (engine: string, daemonId: string | undefined, value: FetchedModelList, runtime?: ModelRuntime) =>
    save(d => ({ ...d, modelsByKey: { ...d.modelsByKey, [modelsKey(engine, daemonId, runtime)]: { value, at: launchClock.now() } } })),
}

// Bumped by storeRepos, so an older fetchRepos answer landing after it is
// not kept over it.
let reposGen = 0

// storeRepos keeps a repository list the sheet fetched itself (after its
// daemon's repos folder changed).
export function storeRepos(value: ReposPart) {
  reposGen++
  save(d => ({ ...d, repos: { value, at: launchClock.now() } }))
}

// patchCachedDaemon keeps a daemon's cached entry in step with a change the
// sheet made (its repos folder), so the next open does not show the old one.
export function patchCachedDaemon(id: string, patch: Partial<DaemonApiInfo>) {
  save(d => d.daemons
    ? { ...d, daemons: { ...d.daemons, value: d.daemons.value.map(x => (x.id === id ? { ...x, ...patch } : x)) } }
    : d)
}

// prefetchLaunchData warms the four answers the sheet opens on — and only
// those (no MCP connections, no tool listings). A part answered within
// FRESH_MS is not asked again. Never rejects.
//
// Not signed in, or the token has lapsed: nothing is asked. A hover must never
// be what sends the page away to renew the session (apiFetch's 401 path).
export function prefetchLaunchData() {
  const token = getAccessToken()
  if (!token || tokenExpired(token)) return
  const c = readLaunchCache()
  const ignore = () => {}
  if (!isFresh(c.repos)) fetchRepos().catch(ignore)
  if (!isFresh(c.daemons)) fetchDaemons().catch(ignore)
  if (!isFresh(c.cluster)) fetchCluster().catch(ignore)
  if (!isFresh(c.creds)) void fetchCreds()
}
