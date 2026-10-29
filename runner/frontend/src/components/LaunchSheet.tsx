import '../../../../packages/settings/settings.css'
import { type ReactNode, useState, useEffect, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { useSessionStore } from '../hooks/useSessionStore'
import { defaultSessionName } from '../lib/sessionName'
import { keepNotice, matchModel, placeWords, type ModelPick } from '../lib/modelKeep'
import { apiFetch } from '../apiFetch'
import { coreOrigin } from '../authClient'
import RunColumn from './RunColumn'
import { workspaceRuntimeAvailable } from '../lib/workspaceCapabilities'
import WorkspacePicker, { type WorkspaceSelection } from './WorkspacePicker'
import McpPicker from './McpPicker'
import LaunchPermissions from './LaunchPermissions'
import { NO_LAUNCH_PERMISSIONS, effectiveLaunchPermissions, type LaunchPermissionsValue } from '../lib/launchPermissions'
import { useToastStore } from '../hooks/useToastStore'
import type { McpSelectionEntry } from '../types'
import { useBackdropClose } from '@blerglab/chat'
import { ENGINE_LABELS, defaultRuntime, runtimeDisabledReason, ACK_AGENT, ACK_TERMINAL } from '../lib/runtimes'
import type { RunRuntime, RunKind, RunEngine } from '../lib/runtimes'
import { useLaunchOptions } from '../lib/launchOptions'
import { useEngineModels, modelCatalogTarget, defaultModel, effectiveEffort, isModelIdFor, isEffortToken, FREE_TEXT_MODEL_ENGINES } from '../lib/engineModels'
import { parseRepoInput, predictCloneFolder, fullName, providerLabel, PROVIDER_LABELS } from '../lib/repoRef'
import type { RepoRef } from '../lib/repoRef'
import { SCRATCH_PREFIX, NO_REPO_LABEL, newScratchSuffix, validScratchSuffix } from '../lib/noRepo'
import {
  readLaunchCache, isFresh, fetchRepos, fetchDaemons, fetchCluster, fetchCreds, fetchMcpConnections,
  launchMcpConnections, launchModelsCache, patchCachedDaemon, storeRepos, reposPartFrom,
} from '../lib/launchCache'
import type { RepoInfo, DaemonApiInfo, ClusterStatusApi, MyCredentialsApi, ReposPart, LaunchCacheData } from '../lib/launchCache'
import { useSessionBoards } from '../lib/sessionBoards'
import './LaunchSheet.css'

function repoKey(r: RepoInfo): string {
  return (r.is_local ? 'local:' + r.name + ':' : '') + (r.provider ?? '') + ':' + r.full_name
}

const CLUSTER_REPO_UNRESOLVED =
  'This folder has no GitHub or GitLab remote the cluster can clone — enter org/name'

interface LaunchSheetProps {
  open: boolean
  onClose: () => void
}

const sectionLabel: React.CSSProperties = {
  color: 'var(--fog)',
  fontSize: '0.7rem',
  fontWeight: 700,
  letterSpacing: '0.1em',
  textTransform: 'uppercase',
  marginBottom: 6,
  display: 'block',
}

const itemBase: React.CSSProperties = {
  background: 'var(--scree)',
  border: '1px solid var(--stone)',
  borderRadius: 8,
  padding: '10px 12px',
  marginBottom: 6,
  cursor: 'pointer',
  color: 'var(--chalk)',
}

// What ticking Unattended changes, said under the box.
const UNATTENDED_HELP =
  'Nobody is watching: the agent decides and finishes on its own instead of discussing and waiting for you.'

// The pre-launch "this will create…" note: a new folder, or No repository's
// scratch folder (or, on the cluster, its empty workspace).
const createPlanStyle: React.CSSProperties = {
  background: 'var(--basalt)',
  border: '1px solid var(--scree-2)',
  borderRadius: 6,
  padding: '8px 12px',
  overflowWrap: 'anywhere',
  marginTop: 8,
  color: 'var(--fog-dim)',
  fontSize: 13,
}

const itemSelected: React.CSSProperties = {
  ...itemBase,
  border: '1px solid var(--selected-border)',
  background: 'var(--selected-bg)',
}

// Remembers the last-used Engine/Model across launches so people aren't
// re-picking every time — per-browser (localStorage), not synced across
// machines. Guarded: localStorage can throw in privacy-restricted contexts.
// What an older version remembered as the launch board; deleted when the sheet opens.
const LEGACY_BOARD_KEY = 'blerg.launch.board'
const LAST_ENGINE_KEY = 'blerg.launch.engine'
const LAST_MODEL_KEY = 'blerg.launch.model'
// The last effort the person explicitly picked. Only an explicit pick is
// stored — a preselected model default never is — so it keeps meaning "I
// chose this" across launches (see effectiveEffort).
const LAST_EFFORT_KEY = 'blerg.launch.effort'

// Model and effort are remembered per engine. Claude keeps the original,
// unsuffixed keys; any other engine gets "<key>.<engine>".
function perEngineKey(base: string, engine: string): string {
  return engine === 'claude' ? base : `${base}.${engine}`
}

// loadStored reads a remembered value that passes valid, else null. Values
// from before the model catalog (the aliases 'sonnet', 'opus', 'haiku',
// 'fable') fail Claude's model-id rule, so they read as "nothing remembered".
function loadStored(key: string, valid: (v: unknown) => v is string): string | null {
  try {
    const v = localStorage.getItem(key)
    return valid(v) ? v : null
  } catch {
    return null
  }
}

function forgetStored(key: string) {
  try {
    localStorage.removeItem(key)
  } catch {
    // ignore
  }
}

function saveDefault(key: string, value: string) {
  try {
    localStorage.setItem(key, value)
  } catch {
    // ignore — best-effort convenience only
  }
}

function hasStoredPreference(key: string): boolean {
  try {
    return localStorage.getItem(key) !== null
  } catch {
    return false
  }
}

// A remembered ID is provisional until launch-context discovery confirms it.
function rememberedEngine(): string {
  return loadStored(LAST_ENGINE_KEY, (value): value is string => typeof value === 'string' && /^[a-z][a-z0-9_-]{0,63}$/.test(value)) ?? 'claude'
}

// The daemon to pre-select so the Run column can speak concretely about it
// (sandbox image, engine availability) before a repo is chosen — and when the
// runtime is the sandbox, one that actually has the image, so the sheet does
// not open on its own "no sandbox image" warning.
function preferredDaemon(list: DaemonApiInfo[], runtime: RunRuntime | null): DaemonApiInfo | null {
  return (runtime === 'docker' ? (list.find(d => d.sandbox_available) ?? list[0]) : list[0]) ?? null
}

// What the sheet opens on, from the first daemons and cluster status it has
// (cached or fetched) — picked once per open; later answers only correct a
// pick that became impossible (see the fetch effect).
//
// The engine is suggested from what the first daemon actually has configured
// — but only when nobody has picked one on this browser yet. Once a person
// picks (or the picker's own saveDefault call fires), their choice always
// wins over this one-time suggestion. Resolved before the runtime, which
// depends on it. The runtime is never the unsandboxed host when something
// safer is available; a remembered OpenClaw lands on This machine rather than
// on a card the sheet then disables — pickEngine does the same when the
// choice is made here instead of remembered.
function pickDefaults(daemonList: DaemonApiInfo[], clusterConfigured: boolean): { engine: RunEngine; runtime: RunRuntime; daemon: DaemonApiInfo | null } {
  let engine: RunEngine = rememberedEngine()
  if (!hasStoredPreference(LAST_ENGINE_KEY)) {
    const available: string[] = daemonList[0]?.available_engines ?? []
    const suggestion = available[0]
    if (suggestion) engine = suggestion
  }
  const runtime = engine === 'openclaw' ? 'daemon' : defaultRuntime(daemonList, clusterConfigured)
  return { engine, runtime, daemon: preferredDaemon(daemonList, runtime) }
}

// Where each of the sheet's four answers stands: still on its way with
// nothing to show ('waiting'), shown from the cache while a refresh is on its
// way ('cached'), answered this open or moments ago ('fresh'), or its
// refresh failed ('failed' — the cached value, if any, stays).
type PartState = 'waiting' | 'cached' | 'fresh' | 'failed'
type PartName = 'repos' | 'daemons' | 'cluster' | 'creds'
const PART_LABELS: Record<PartName, string> = {
  repos: 'repositories', daemons: 'daemons', cluster: 'cluster', creds: 'credentials',
}

function partState(p: LaunchCacheData[PartName]): PartState {
  return !p ? 'waiting' : isFresh(p) ? 'fresh' : 'cached'
}

function RepositoryActions({modern,hidden,children}:{modern:boolean;hidden:boolean;children:ReactNode}) {
 return modern ? <div hidden={hidden}>{children}</div> : <>{children}</>
}

export default function LaunchSheet({ open, onClose }: LaunchSheetProps) {
  const navigate = useNavigate()
  const addPendingSession = useSessionStore(s => s.addPendingSession)
  const backdrop = useBackdropClose(onClose)

  // The parent remounts this component (via a `key` tied to `open`) each time
  // the sheet opens, so all form state below starts fresh from its initial
  // value — no synchronous state resets in an effect (which the
  // react-hooks/set-state-in-effect rule disallows). What the sheet last knew
  // (lib/launchCache) seeds it, read once here at mount, and the defaults are
  // picked from it at once when it has daemons or a cluster status.
  const [boot] = useState(() => {
    // The closed sheet (mounted hidden) uses none of it, and so asks nothing.
    const cache: LaunchCacheData = open ? readLaunchCache() : { modelsByKey: {} }
    return {
      cache,
      parts: {
        repos: partState(cache.repos),
        daemons: partState(cache.daemons),
        cluster: partState(cache.cluster),
        creds: partState(cache.creds),
      } satisfies Record<PartName, PartState>,
      hasAny: !!(cache.repos || cache.daemons || cache.cluster || cache.creds),
      defaults: cache.daemons || cache.cluster
        ? pickDefaults(cache.daemons?.value ?? [], !!cache.cluster?.value.configured)
        : null,
    }
  })
  const [parts, setParts] = useState<Record<PartName, PartState>>(boot.parts)
  const [repos, setRepos] = useState<RepoInfo[]>(() => boot.cache.repos?.value.repos ?? [])
  const [daemons, setDaemons] = useState<DaemonApiInfo[]>(() => boot.cache.daemons?.value ?? [])
  const [search, setSearch] = useState('')
  const [selectedRepo, setSelectedRepo] = useState<RepoInfo | null>(null)
  const [workspacePick, setWorkspacePick] = useState<{scope: string; selection: WorkspaceSelection | null; names: string[]}>({scope: '', selection: null, names: []})
  const [desktopScope, setDesktopScope] = useState(0)

  // "No repository" is the default pick: a session tied to no repository.
  // On the cluster that is an empty, throwaway workspace; on a daemon it is a
  // new scratch folder under the repos root, named for the person
  // (scratchSuffix, renamable) so Launch needs nothing more.
  const [noRepoMode, setNoRepoMode] = useState(true)
  const [scratchSuffix, setScratchSuffix] = useState(() => newScratchSuffix())
  const [scratchRenaming, setScratchRenaming] = useState(false)
  const [repoActions, setRepoActions] = useState(false)
  const [pickerReset, setPickerReset] = useState(0)
  const [newFolderMode, setNewFolderMode] = useState(false)
  // Visibility of a new repository created on the cluster (private by default).
  const [visibility, setVisibility] = useState<'private' | 'public'>('private')
  // A repository nobody has checked out and nobody listed is still
  // launchable: a cluster pod clones it, and so does a daemon (into its
  // repos folder). freeTextMode is "use exactly what I typed" — owner/name,
  // or a pasted URL. freeTextProvider is the provider a bare owner/name is
  // read on; a URL names its own.
  const [freeTextMode, setFreeTextMode] = useState(false)
  const [freeTextProvider, setFreeTextProvider] = useState('github')
  const [selectedDaemon, setSelectedDaemon] = useState<DaemonApiInfo | null>(() => boot.defaults?.daemon ?? null)
  useEffect(() => {
    const invalidate = () => {
      setDesktopScope(n => n + 1); setNoRepoMode(false); setSelectedRepo(null); setSelectedDaemon(null); setDaemons([]); setRepos([])
      void fetchDaemons().then(list => { setDaemons(list); if (list.length === 1) setSelectedDaemon(list[0]) }).catch(() => setDaemons([]))
      void fetchRepos().then(part => setRepos(part.repos)).catch(() => setRepos([]))
    }
    window.addEventListener('blerg-desktop-scope-changed', invalidate)
    window.addEventListener('blerg-desktop-inventory-changed', invalidate)
    return () => { window.removeEventListener('blerg-desktop-scope-changed', invalidate); window.removeEventListener('blerg-desktop-inventory-changed', invalidate) }
  }, [])
  const [initialPrompt, setInitialPrompt] = useState('')
  // MCP servers and tools chosen for this session (McpPicker). Empty: none, and no `mcp` is sent.
  const boards = useSessionBoards(open)
  // Never remembered: a launch board gives the session Full access to it, so every launch starts
  // with none (#302); board access can be given in the session's Permissions later.
  const [boardId, setBoardId] = useState('')
  useEffect(() => { forgetStored(LEGACY_BOARD_KEY) }, [])
  function pickBoard(id: string) {
    setBoardId(id)
  }
  const [mcp, setMcp] = useState<McpSelectionEntry[]>([])
  // Permissions given before the session starts (LaunchPermissions); only what still applies is sent.
  const [permissions, setPermissions] = useState<LaunchPermissionsValue>(NO_LAUNCH_PERMISSIONS)
  // Session kind: the native agent harness (structured chat transcript, no
  // terminal) or the classic tmux-hosted terminal CLI. Agent is the default —
  // it is what this tool is for, and it is what the sandbox now covers.
  const [kind, setKind] = useState<RunKind>('agent')
  // Which CLI actually drives the session. Orthogonal to kind/runtime — either
  // engine can run as a terminal or agent session, on any runtime below.
  const [engine, setEngine] = useState<RunEngine>(() => boot.defaults?.engine ?? rememberedEngine())
  // Model + effort, for whichever engine is picked (the list itself is read
  // below, once the runtime and daemon are known). pickedModels[engine] is
  // what the person chose from that engine's list (else what was
  // remembered); it only counts while the list still has it, else the
  // engine's default model stands in. explicitEfforts likewise: it wins while
  // the selected model supports it, and otherwise the model's own default is
  // preselected — so changing model re-preselects unless an explicit,
  // still-supported choice exists. A model typed by hand (no list) is kept
  // apart, in typedModel below.
  // Seeded once, at mount, from what each engine remembered — localStorage is
  // read here and nowhere else during render.
  const [pickedModels, setPickedModels] = useState<Record<string, string | null>>(() =>
    Object.fromEntries([rememberedEngine()].map(e => [e, loadStored(perEngineKey(LAST_MODEL_KEY, e), v => isModelIdFor(e, v))])))
  const [explicitEfforts, setExplicitEfforts] = useState<Record<string, string | null>>(() =>
    Object.fromEntries([rememberedEngine()].map(e => [e, loadStored(perEngineKey(LAST_EFFORT_KEY, e), isEffortToken)])))
  // The display name of each engine's pick (from the list it was picked in), to find the same model
  // under another routing id in another place's list.
  const [pickedNames, setPickedNames] = useState<Record<string, string>>({})
  const pickedModel = pickedModels[engine] ?? null
  const explicitEffort = explicitEfforts[engine] ?? null
  // Where a session actually runs, for both kinds (spec §2). Without cached
  // data the initial value is only a placeholder: the real default lands from
  // pickDefaults() once /api/daemons and /api/cluster/status answer, and never
  // picks the unsandboxed host when something safer is available.
  const [runtime, setRuntime] = useState<RunRuntime>(() => boot.defaults?.runtime ?? 'daemon')
  // The model list: GET /api/models/{engine}, asked about the selected daemon
  // (Codex's and Hermes's lists are what that daemon probed; Claude's ignores
  // it). The engine's built-in list shows until it answers, and if it fails.
  // Cluster Codex asks its own runtime to probe with the launching account’s
  // credentials; an unscoped daemon catalog is never used for cluster. On a
  // daemon runtime it waits until a daemon is selected rather than ask
  // without one.
  const workspaceScope = `${desktopScope}:${runtime}:${selectedDaemon?.id ?? ''}:${selectedDaemon?.ownership_revision ?? ''}:${selectedDaemon?.connection_generation ?? ''}`
  const onCluster = runtime === 'cluster'
  const listDaemonId = onCluster ? undefined : selectedDaemon?.id
  // Asked at once — of the cached daemon, when the sheet opened from cache —
  // and each answer is kept per engine and daemon for the next open.
  const catalogTarget = modelCatalogTarget(runtime, listDaemonId)
  const launchOptions = useLaunchOptions({ runtime, daemonId: listDaemonId, kind, enabled: open && (onCluster || !!selectedDaemon) })
  const engineKnown = launchOptions.state === 'known' && launchOptions.engines.some(e => e.id === engine)
  // Card #151: a change of where it runs keeps the person's model and effort (each place's list is
  // matched against the pick below). It used to clear them here, so the place's default won.
  // An engine this place does not offer gives way to one it does, said aloud; the picks are kept
  // for when the person comes back.
  const [engineNotice, setEngineNotice] = useState<{ text: string; engine: string } | null>(null)
  if (launchOptions.state === 'known' && launchOptions.engines.length > 0 && !launchOptions.engines.some(e => e.id === engine)) {
    const to = launchOptions.engines[0]
    setEngineNotice({ engine: to.id, text: `${ENGINE_LABELS[engine] ?? engine} isn't available ${placeWords(runtime, selectedDaemon?.name)}: switched to ${to.name || ENGINE_LABELS[to.id] || to.id}` })
    setEngine(to.id)
  }
  const engineModels = useEngineModels(engine, catalogTarget.daemonId, { enabled: open && engineKnown, cache: launchModelsCache, runtime: catalogTarget.runtime })
  const listModels = engineModels.models
  const pick: ModelPick | null = pickedModel ? { id: pickedModel, ...(pickedNames[engine] ? { name: pickedNames[engine] } : {}) } : null
  const modelMatch = matchModel(pick, listModels)
  const selectedModel = modelMatch?.model ?? defaultModel(engine, listModels)
  // No list for an engine whose CLI takes a model name (an older daemon, a
  // probe that found nothing, the cluster): an optional free-text name,
  // checked against the engine's model rule before it can be sent, and — when
  // the engine takes effort — an effort from its allowlist, Auto by default.
  // Its own state, never the list pick's, and it starts empty again whenever
  // the engine or where it runs changes.
  const typedKey = `${engine}\n${runtime}\n${listDaemonId ?? ''}`
  const [typed, setTyped] = useState<{ key: string, model: string, effort: string }>({ key: typedKey, model: '', effort: '' })
  // Engine, runtime or daemon changed: start the typed name over (React's
  // adjust-state-while-rendering pattern — no effect, no stale frame).
  if (typed.key !== typedKey) setTyped({ key: typedKey, model: '', effort: '' })
  const typedHere = typed.key === typedKey ? typed : { key: typedKey, model: '', effort: '' }
  // A background catalog must never replace a model the person already typed.
  const freeTextModelOffered = (listModels.length === 0 && !engineModels.loading || typedHere.model.trim() !== '') && FREE_TEXT_MODEL_ENGINES.has(engine)
  const freeTextModel = freeTextModelOffered ? typedHere.model.trim() : ''
  const freeTextModelInvalid = freeTextModel !== '' && !isModelIdFor(engine, freeTextModel)
  const freeTextEffort = freeTextModelOffered && engineModels.engineEfforts.includes(typedHere.effort) ? typedHere.effort : undefined
  const selectedEffort = freeTextModelOffered ? freeTextEffort : effectiveEffort(selectedModel, explicitEffort)
  // The person's model or effort is not what will run here: say so beside the field. Not while the
  // place's list is still loading (the built-in list stands in): a late list must not count as a
  // swap, and the pick is kept, so the list that lands brings it back.
  const modelNotice = freeTextModelOffered || engineModels.loading || listModels.length === 0 ? null
    : keepNotice({ pick, match: modelMatch, selected: selectedModel, explicitEffort, effort: selectedEffort, place: placeWords(runtime, selectedDaemon?.name) })
  // Once someone picks a runtime, no suggested default ever moves it again for
  // the life of this sheet. A ref, not state: nothing renders from it, and it
  // is only read inside event handlers/effects.
  const runtimePickedRef = useRef(false)
  // Likewise a daemon the person picked: a refresh never swaps it for another
  // (if it has gone, Launch says so instead).
  const daemonPickedRef = useRef(false)
  // The Repository search box doubles as the free-text org/name entry on
  // cluster runtime; the unresolved-folder message focuses it.
  const repoInputRef = useRef<HTMLInputElement>(null)
  // Bypassing engine permission prompts defaults OFF (R5): it's only ever
  // allowed inside the Docker sandbox for a terminal session, so it's forced
  // off anywhere else (see the effect below) rather than merely hidden.
  const [dangerouslySkipPermissions, setDangerouslySkipPermissions] = useState(false)
  // "This machine, unsandboxed" runs as you, with full filesystem access — an
  // explicit, required acknowledgement rather than a silent default.
  const [agentAck, setAgentAck] = useState(false)
  // Unattended: nobody will be reading the chat, so the agent decides and
  // finishes on its own. Off by default — a session launched from here has
  // its person in front of it. An agent session's choice only: a terminal
  // session has no chat to wait in.
  const [unattended, setUnattended] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // The name the person typed; null until they do. Until then the field shows the default, the
  // repository's name (it follows a change of repository), numbered past running sessions.
  const [typedName, setTypedName] = useState<string | null>(null)
  const [clusterStatus, setClusterStatus] = useState<ClusterStatusApi | null>(() => boot.cache.cluster?.value ?? null)
  const [myCreds, setMyCreds] = useState<MyCredentialsApi | null>(() => boot.cache.creds?.value ?? null)
  // GET /api/repos answered with stale_at: a GitHub/GitLab list (usually the
  // person's own, from their token) failed to refresh — typically a revoked
  // or mistyped token. Without saying so the picker just looks empty.
  const [reposStale, setReposStale] = useState(() => boot.cache.repos?.value.stale ?? false)
  const [reposStaleDismissed, setReposStaleDismissed] = useState(false)
  // The list has arrived (possibly empty), or failed: until then the column
  // shows a skeleton, and the empty-state hint waits so it doesn't flash.
  const [reposLoaded, setReposLoaded] = useState(() => !!boot.cache.repos)
  // The latest picks, for the fetch callbacks below, which outlive the render
  // that started them (written in an effect, never during render).
  const latest = useRef({ runtime, engine, kind })
  useEffect(() => { latest.current = { runtime, engine, kind } })
  // The body shows at once from anything cached; on a cold start as soon as
  // daemons and cluster status have answered (the repository list streams in
  // after, under a skeleton), never waiting on the repository list.
  const showBody = boot.hasAny || (parts.daemons !== 'waiting' && parts.cluster !== 'waiting')
  const refreshingParts = (Object.keys(parts) as PartName[]).filter(p => parts[p] === 'cached')

  // A daemon's repos folder change (applyReposRoot below) refetches the list;
  // only the latest answer counts — the mount's own included.
  const reposRefetch = useRef(0)

  // Four independent requests — none waits for another. A part answered
  // within FRESH_MS (a prefetch on hovering "+ New") is not asked again.
  useEffect(() => {
    if (!open) return
    let live = true
    const setPart = (name: PartName, st: PartState) => setParts(p => ({ ...p, [name]: st }))
    const loadFailed = (err: unknown) => setError('Failed to load data: ' + (err instanceof Error ? err.message : String(err)))
    // The daemons and cluster status known so far, for picking defaults
    // (once, from the first of both) and correcting picks.
    const known = {
      daemons: boot.cache.daemons?.value,
      cluster: boot.cache.cluster?.value,
      daemonsSettled: !!boot.cache.daemons,
      clusterSettled: !!boot.cache.cluster,
    }
    let defaultsApplied = !!boot.defaults

    const settle = () => {
      if (!known.daemonsSettled || !known.clusterSettled) return
      const daemonList = known.daemons ?? []
      const clusterConfigured = !!known.cluster?.configured
      if (!defaultsApplied) {
        defaultsApplied = true
        const d = pickDefaults(daemonList, clusterConfigured)
        if (!hasStoredPreference(LAST_ENGINE_KEY)) setEngine(d.engine)
        // Only when nobody has picked a runtime yet, so a refresh can never
        // move a choice out from under someone (spec §1).
        if (!runtimePickedRef.current) setRuntime(d.runtime)
        const preferred = preferredDaemon(daemonList, runtimePickedRef.current ? null : d.runtime)
        if (preferred) setSelectedDaemon(preferred)
        return
      }
      // Later answers only correct a pick that became impossible, and never
      // one the person made.
      const { runtime: rt, engine: eng, kind: k } = latest.current
      let nextRuntime = rt
      if (!runtimePickedRef.current &&
          runtimeDisabledReason(rt, { clusterConfigured, daemons: daemonList, engine: eng, kind: k }) !== null) {
        nextRuntime = eng === 'openclaw' ? 'daemon' : defaultRuntime(daemonList, clusterConfigured)
        if (nextRuntime !== rt) setRuntime(nextRuntime)
      }
      setSelectedDaemon(cur => {
        const now = cur && daemonList.find(d => d.id === cur.id)
        if (now) return now
        if (cur && daemonPickedRef.current) return cur
        return preferredDaemon(daemonList, nextRuntime)
      })
    }

    if (boot.parts.repos !== 'fresh') {
      const seq = reposRefetch.current
      fetchRepos()
        .then((v: ReposPart) => {
          if (!live) return
          // Overtaken by a repos-folder change: that answer is the list now.
          if (seq === reposRefetch.current) {
            setRepos(v.repos)
            setReposStale(v.stale)
          }
          setReposLoaded(true)
          setPart('repos', 'fresh')
        })
        .catch(err => {
          if (!live) return
          setReposLoaded(true)
          setPart('repos', 'failed')
          if (!boot.cache.repos) loadFailed(err)
        })
    }
    if (boot.parts.daemons !== 'fresh') {
      fetchDaemons()
        .then(list => {
          if (!live) return
          setDaemons(list)
          known.daemons = list
          known.daemonsSettled = true
          setPart('daemons', 'fresh')
          settle()
        })
        .catch(err => {
          if (!live) return
          known.daemonsSettled = true
          setPart('daemons', 'failed')
          if (!boot.cache.daemons) loadFailed(err)
          settle()
        })
    }
    if (boot.parts.cluster !== 'fresh') {
      fetchCluster()
        .then(c => {
          if (!live) return
          setClusterStatus(c)
          known.cluster = c
          known.clusterSettled = true
          setPart('cluster', 'fresh')
          settle()
        })
        .catch(err => {
          if (!live) return
          known.clusterSettled = true
          setPart('cluster', 'failed')
          if (!boot.cache.cluster) loadFailed(err)
          settle()
        })
    }
    if (boot.parts.creds !== 'fresh') {
      fetchCreds().then(c => {
        if (!live) return
        setMyCreds(c)
        setPart('creds', 'fresh')
      })
    }
    // The MCP connection list, in parallel with the rest: McpPicker shows it
    // (from cache at once) when its section mounts, and shares this request.
    if (!isFresh(boot.cache.mcpConnections)) fetchMcpConnections().catch(() => { /* McpPicker says so */ })
    return () => { live = false }
  }, [open, boot])

  // A daemon's repos folder was changed from the Run column: every "will
  // clone to …" hint must now name the new folder, and the repo list must be
  // what is on disk there — the server already has it from the daemon.
  // Changes in quick succession each refetch; only the latest answer counts,
  // so a slow earlier one (or the sheet's first fetch) can't put back the
  // previous folder's list.
  const applyReposRoot = (daemonId: string, reposRoot: string) => {
    setDaemons(ds => ds.map(d => (d.id === daemonId ? { ...d, repos_root: reposRoot } : d)))
    setSelectedDaemon(d => (d && d.id === daemonId ? { ...d, repos_root: reposRoot } : d))
    patchCachedDaemon(daemonId, { repos_root: reposRoot })
    const seq = ++reposRefetch.current
    apiFetch('/api/repos')
      .then(reposPartFrom)
      .then(next => {
        if (seq !== reposRefetch.current) return
        setRepos(next.repos)
        setReposStale(next.stale)
        storeRepos(next)
      })
      .catch(() => { /* the old list stays; the next open refreshes it */ })
  }

  // Bypass is Docker-only (R5) — force it off the instant runtime moves away
  // from 'docker', so a stale toggle state can never reach the server.
  // Agent-kind sessions never prompt at all, so the flag is meaningless
  // there; outside the sandbox it is simply not allowed.
  if ((runtime !== 'docker' || kind === 'agent') && dangerouslySkipPermissions) setDangerouslySkipPermissions(false)
  // "New folder" isn't offered on cluster runtime, so a
  // selection made before the switch must not survive it invisibly.

  // The authenticated launch-context snapshot is authoritative for this target;
  // legacy operator/personal metadata is still used for clone hints, not engine choices.
  const availableEnginesHere: string[] | null = launchOptions.state === 'known' ? launchOptions.engines.map(e => e.id) : null

  const engineMissingHere = availableEnginesHere !== null && !availableEnginesHere.includes(engine)
  // A hard block is only honest when we actually know what this person has —
  // as of now: with myCreds unknown, or only cached and still refreshing, the
  // softer advisory warning stands instead.
  const clusterFacts = parts.creds === 'fresh' && parts.cluster === 'fresh'
  const clusterEngineBlocked = runtime === 'cluster' && myCreds !== null && engineMissingHere && clusterFacts
  // Neither the person's own GitHub credential nor a shared operator token:
  // the pod can still clone public repos, so this warns rather than blocks.
  // Nothing is cloned with No repository, so it does not apply there.
  const clusterGitMissing =
    runtime === 'cluster' && !noRepoMode && myCreds !== null && !myCreds.git && !clusterStatus?.git_configured

  const settingsLink = (
    <a
      href={`${coreOrigin()}/settings`}
      target="_blank"
      rel="noopener noreferrer"
      style={{ color: 'inherit', textDecoration: 'underline' }}
    >
      Settings
    </a>
  )

  function selectNoRepo() {
    setSelectedRepo(null)
    setNewFolderMode(false)
    setFreeTextMode(false)
    setNoRepoMode(true)
  }

  function selectRepo(repo: RepoInfo) {
    setSelectedRepo(repo)
    setNoRepoMode(false)
    setNewFolderMode(false)
    setFreeTextMode(false)
    if (daemons.length > 0) {
      const checkedOutDaemon = daemons.find(d => repo.checked_out.includes(d.name))
      setSelectedDaemon(checkedOutDaemon ?? daemons[0])
    }
  }

  function selectNewFolder() {
    setSelectedRepo(null)
    setNoRepoMode(false)
    setNewFolderMode(true)
    setFreeTextMode(false)
    if (daemons.length > 0 && !selectedDaemon) {
      setSelectedDaemon(daemons[0])
    }
  }

  function selectFreeTextRepo() {
    setSelectedRepo(null)
    setNoRepoMode(false)
    setNewFolderMode(false)
    setFreeTextMode(true)
  }

  const folderName = search.trim()
  // The repository the session will run in, by name, for its default name.
  const repoForName = noRepoMode ? '' : selectedRepo ? (selectedRepo.name || selectedRepo.full_name) : newFolderMode || freeTextMode ? folderName : workspacePick.scope === workspaceScope ? workspacePick.names[0] ?? '' : ''
  const sessions = useSessionStore(s => s.sessions)
  // Reuse the scoped session list; do not retain desktop paths in another cache.
  const recentRepos = [...sessions]
    .filter(s => runtime === 'cluster' ? s.runtime === 'cluster' : s.daemon_id === selectedDaemon?.id)
    .sort((a, b) => b.started_at.localeCompare(a.started_at))
    .flatMap(s => {
      if (!s.repo) return []
      const prefix = `${selectedDaemon?.repos_root}/`
      return [runtime !== 'cluster' && s.project_path.startsWith(prefix) ? s.project_path.slice(prefix.length) : s.repo]
    })
  const runningTitles = sessions.filter(s => !s.archived_at && !s.ended_at && !['ended', 'error', 'stopped'].includes(s.status)).map(s => s.title ?? '')
  const defaultName = defaultSessionName(repoForName, runningTitles)
  const name = typedName ?? defaultName
  // What the Repository box says, read as a repository: owner/name (on the
  // picked provider) or a pasted URL (on its host's). null for anything else.
  const typedRef: RepoRef | null = parseRepoInput(folderName, freeTextProvider)
  const onDaemonRuntime = runtime !== 'cluster'

  const filteredRepos = repos.filter(r => runtime !== 'cluster' || !r.is_local).filter(r =>
    search === '' ||
    r.name.toLowerCase().includes(search.toLowerCase()) ||
    r.full_name.toLowerCase().includes(search.toLowerCase()) ||
    // A pasted URL of a listed repository finds it too.
    (!!typedRef?.url && r.full_name.toLowerCase() === fullName(typedRef).toLowerCase())
  )

  const isCheckedOut = !!(
    selectedRepo &&
    selectedDaemon &&
    selectedRepo.checked_out.includes(selectedDaemon.name)
  )

  // No exact match in the list means the typed text can only be a repo to
  // clone by name — offer it explicitly rather than leaving a dead end at "No
  // existing repos match." The cluster takes any text (a bare name resolves
  // against the server's org); a daemon needs an owner/name or a URL.
  const typedRepoUnmatched =
    !newFolderMode &&
    folderName !== '' &&
    (runtime === 'cluster' || typedRef !== null) &&
    !repos.some(r => r.name === folderName || r.full_name === folderName)

  // No repository on a daemon is a real folder: its (renamable) name must be
  // one the server accepts. The cluster has no folder to name.
  const scratchFolder = SCRATCH_PREFIX + scratchSuffix
  const scratchNameInvalid = noRepoMode && onDaemonRuntime && !validScratchSuffix(scratchSuffix)

  // New repository on the cluster: the text must read as owner/name (not a
  // URL — the server creates it on the picked provider, it has no host yet).
  const newRepoRef: RepoRef | null = runtime === 'cluster' && newFolderMode && typedRef && !typedRef.url ? typedRef : null
  // No token for that provider: the server will try, fail, and start the
  // session in an empty repository instead — a warning, not a block.
  const newRepoTokenMissing =
    !!newRepoRef && myCreds !== null && !(myCreds.git_providers ?? []).includes(newRepoRef.provider)

  const workspaceRuntimeReady = workspaceRuntimeAvailable(runtime === 'cluster' ? clusterStatus?.workspace_capabilities : selectedDaemon?.workspace_capabilities, runtime)
  const useWorkspacePicker = workspaceRuntimeReady && Boolean(runtime === 'cluster' || selectedDaemon?.discovery_version === 1)
  const workspaceSelection = workspacePick.scope === workspaceScope ? workspacePick.selection : null

  const hasRepoChoice =
    workspaceSelection !== null ||
    (noRepoMode && !scratchNameInvalid) ||
    selectedRepo !== null ||
    (newFolderMode && folderName !== '' && (runtime !== 'cluster' || newRepoRef !== null)) ||
    (freeTextMode && folderName !== '' && (runtime === 'cluster' || typedRef !== null))

  // A daemon clone of a named repository (a typed one, or a listed one that
  // isn't on this daemon yet) goes to a folder the server picks by the same
  // rule predictCloneFolder mirrors — so the sheet can say which, and warn
  // when an unrelated same-named folder pushes it aside. Needs a daemon that
  // can clone by name; an older one keeps the legacy listed-repo clone.
  const daemonClonesByName = !!selectedDaemon?.clone_from
  const listedCloneRef: RepoRef | null =
    onDaemonRuntime && daemonClonesByName && !newFolderMode && !freeTextMode &&
    !!selectedRepo && !selectedRepo.is_local && !isCheckedOut && !!selectedRepo.provider &&
    selectedRepo.full_name.split('/').length === 2
      ? { provider: selectedRepo.provider, owner: selectedRepo.full_name.split('/')[0], name: selectedRepo.full_name.split('/')[1] }
      : null
  const freeTextCloneRef: RepoRef | null = onDaemonRuntime && freeTextMode ? typedRef : null
  const cloneRef = freeTextCloneRef ?? listedCloneRef
  const clonePlan = cloneRef && selectedDaemon && daemonClonesByName
    ? predictCloneFolder(cloneRef, selectedDaemon.name, repos)
    : null
  const clonePlanProblem = clonePlan && 'problem' in clonePlan ? clonePlan.problem : null
  const cloneFolder = clonePlan && 'folder' in clonePlan ? clonePlan : null
  const cloneByNameUnsupported =
    freeTextCloneRef && selectedDaemon && !daemonClonesByName
      ? `${selectedDaemon.name} can't clone a repository by name yet — update its daemon, or clone it into ${selectedDaemon.repos_root} yourself.`
      : null
  // No token of this person's for the repository's provider: a public
  // repository still clones, and so does one this machine can reach with its
  // own git access — so this advises, it never blocks (as clusterGitMissing).
  // On This machine a person's token is used for the clone only where the
  // daemon's owner allowed it: the clone runs on the bare host, where any
  // other session could read it. A public repository is unaffected; a private
  // one is refused by the server with the same reason — said here first.
  const cloneHostTokenWithheld =
    !!cloneRef && !cloneFolder?.present && runtime === 'daemon' &&
    !!selectedDaemon && !selectedDaemon.allow_host_credential_clone
  const cloneGitMissing =
    !!cloneRef && !cloneFolder?.present && myCreds !== null && !cloneHostTokenWithheld &&
    !(myCreds.git_providers ?? []).includes(cloneRef.provider)
  // A local-only folder with no known GitHub remote has nothing a cluster pod
  // can clone: its full_name is just the folder name, and sending that bare
  // name used to come back as "repo must be given as org/name". Ask for the
  // org/name instead of guessing it from the folder name (which can differ).
  const clusterRepoUnresolved =
    runtime === 'cluster' && !newFolderMode && !freeTextMode &&
    !!selectedRepo?.is_local && selectedRepo.cloneable === false
  // Cluster sessions run as a Job the server creates itself — they need no
  // daemon at all, which is exactly why they're the default when none is up.
  const needsDaemon = runtime !== 'cluster'

  // Why the selected runtime cannot be launched on right now, or null. It is
  // the same rule the Run column disables its cards with — asked again here
  // because a card can go disabled *after* it was selected (pick OpenClaw, or
  // switch to Terminal on the cluster card), and a disabled card left selected
  // must block Launch rather than post a request the server will refuse. It
  // subsumes the old OpenClaw-in-sandbox and cluster+Terminal special cases.
  const runtimeBlockedReason = runtimeDisabledReason(runtime, {
    clusterConfigured: !!clusterStatus?.configured, daemons, engine, kind,
  })

  // "This machine + Agent + Claude" needs the daemon's claude CLI or an API
  // key. Only a daemon that says it has neither is refused.
  const hostClaudeBlockedReason =
    runtime === 'daemon' && kind === 'agent' && engine === 'claude' && selectedDaemon &&
    selectedDaemon.claude_cli_available === false && selectedDaemon.anthropic_key_set === false
      ? `${selectedDaemon.name} can't run a Claude agent session: install Claude Code and log in (\`claude\` on the daemon's PATH), or set ANTHROPIC_API_KEY for the daemon — or pick another runtime or engine.`
      : null
  // "Local sandbox + Agent + Claude" needs a credential the container can
  // use; the daemon refuses the spawn without one. Said here, before Launch,
  // as an advisory (the server is the real gate) — and only when the daemon
  // says so outright, never guessed.
  const sandboxClaudeMissing =
    runtime === 'docker' && kind === 'agent' && engine === 'claude' && !!selectedDaemon &&
    selectedDaemon.sandbox_claude_credential === false
  const scratchNameProblem = scratchNameInvalid
    ? `The scratch folder name after ${SCRATCH_PREFIX} must be 1-64 letters, digits, '-' or '_', starting with a letter or digit.`
    : null
  // A daemon from the cache may have gone since: Launch waits for a fresh
  // /api/daemons to say the selected one is still connected (the server
  // refuses an unknown daemon anyway; this says so before Launch).
  const daemonGone = needsDaemon && !!selectedDaemon && parts.daemons === 'fresh' && !daemons.some(d => d.id === selectedDaemon.id)
  const daemonUnconfirmedReason = !needsDaemon || !selectedDaemon
    ? null
    : daemonGone
      ? `${selectedDaemon.name} is no longer connected — pick another daemon.`
      : parts.daemons === 'fresh'
        ? null
        : parts.daemons === 'failed'
          ? `Couldn't check that ${selectedDaemon.name} is still connected — close and reopen to try again.`
          : `Checking that ${selectedDaemon.name} is still connected…`
  const mcpEngineAvailable = launchOptions.engines.find(e => e.id === engine)?.restricted === true
  const mcpAvailable = (runtime === 'cluster' || runtime === 'docker') && mcpEngineAvailable && kind === 'agent'
  // The board alone may also go to a session on This machine, when its daemon dials the
  // gateway through the server (connections never do).
  const boardOnHost = runtime === 'daemon' && selectedDaemon?.mcp_gateway_via_server === true && mcpEngineAvailable && kind === 'agent'
  const boardProblem = !boardId ? null
    : !mcpAvailable && !boardOnHost ? 'Clear the board or choose an agent session in a supported sandbox to use its tools.'
    : boards.state !== 'ready' ? 'Checking your board access — retry or clear the board to launch without it.'
    : !boards.configured || !boards.boards.some(b => b.id === boardId) ? 'This board is no longer available to you. Choose another board or clear the board.'
    : null
  const blockedReason = boardProblem ?? runtimeBlockedReason ?? hostClaudeBlockedReason ?? cloneByNameUnsupported ?? clonePlanProblem ?? scratchNameProblem ?? daemonUnconfirmedReason


  const canSubmit = engineKnown && hasRepoChoice &&
    (!workspaceSelection || (workspaceRuntimeReady && (runtime === 'cluster' || daemons.some(d => d.id === selectedDaemon?.id && d.connection_generation === selectedDaemon?.connection_generation)))) &&
    (!needsDaemon || selectedDaemon !== null) && !submitting &&
    (runtime !== 'daemon' || agentAck) && !clusterEngineBlocked && blockedReason === null &&
    !clusterRepoUnresolved && !freeTextModelInvalid

  // ── Run column handlers ────────────────────────────────────────────────────
  // Any deliberate pick pins the runtime for the life of this sheet.
  function pickRuntime(rt: RunRuntime) {
    setRuntime(rt)
    runtimePickedRef.current = true
    // The acknowledgement is about one specific runtime; re-consent when the
    // question changes rather than carrying a tick across it.
    setAgentAck(false)
  }

  function pickKind(k: RunKind) {
    setKind(k)
    // The acknowledgement's wording differs per kind, so it must be re-read.
    setAgentAck(false)
  }

  function pickEngine(e: RunEngine) {
    setEngine(e)
    setEngineNotice(null)
    saveDefault(LAST_ENGINE_KEY, e)
    // OpenClaw only runs on the bare daemon host — no sandbox image, no
    // cluster image. Move rather than strand the sheet on a disabled card.
    if (e === 'openclaw' && runtime !== 'daemon') {
      setRuntime('daemon')
      runtimePickedRef.current = true
      setAgentAck(false)
    }
  }

  function pickModel(id: string) {
    setPickedModels(p => ({ ...p, [engine]: id }))
    const name = listModels.find(m => m.id === id)?.name
    if (name) setPickedNames(p => ({ ...p, [engine]: name }))
    saveDefault(perEngineKey(LAST_MODEL_KEY, engine), id)
  }

  // The free-text model name and its effort ("" = Auto): kept as typed, for
  // this engine and place only (typedKey), never remembered or shared with
  // the list pick.
  function typeModel(v: string) {
    setTyped({ ...typedHere, model: v })
  }

  function pickTypedEffort(e: string) {
    setTyped({ ...typedHere, effort: e })
  }

  function pickEffort(e: string) {
    setExplicitEfforts(p => ({ ...p, [engine]: e }))
    saveDefault(perEngineKey(LAST_EFFORT_KEY, engine), e)
  }

  async function handleSubmit() {
    if (!canSubmit || (needsDaemon && !selectedDaemon)) return
    // A cluster pod resolves the repo into a clone URL, and a bare name is
    // only resolvable when the server has an org configured — picking a repo
    // out of the list and getting "repo must be org/name" back is the bug
    // this avoids. The daemon path keeps the bare name: it addresses repos by
    // directory under its repos root.
    // A daemon clones a listed repository that is not on disk yet. A GitHub
    // one keeps the historical bare name (the daemon qualifies it with its
    // org); any other provider's goes as owner/name with its provider, so
    // the daemon clones it from that host — never a same-named GitHub repo.
    if (workspaceSelection) {
      await postSession({ selection: workspaceSelection, daemon_id: selectedDaemon?.id })
      return
    }
    if (noRepoMode) {
      await postSession({
        daemon_id: selectedDaemon?.id ?? '',
        // Explicit: the server never reads a blank repo as "no repository".
        no_repo: true,
        // The name the sheet showed before Launch; the cluster has no folder.
        scratch_folder: onDaemonRuntime ? scratchFolder : undefined,
      })
      return
    }
    const daemonClone =
      runtime !== 'cluster' && !newFolderMode && !freeTextMode &&
      !!selectedRepo && !selectedRepo.is_local && !isCheckedOut &&
      !!selectedRepo.provider && selectedRepo.provider !== 'github'
    let repoName = newFolderMode || freeTextMode
      ? folderName
      : runtime === 'cluster' || daemonClone
        ? (selectedRepo!.full_name || selectedRepo!.name)
        : selectedRepo!.name
    // The picked repository's provider tells the server which host to clone
    // from (and which of the person's tokens to use). New-folder entries
    // carry none: the server's GitHub default applies. A folder already on
    // the daemon needs no clone, so no provider is sent for it.
    let repoProvider = (runtime === 'cluster' && !newFolderMode && !freeTextMode) || daemonClone
      ? selectedRepo?.provider
      : undefined
    // A new repository on the cluster is created on the picked provider.
    if (newRepoRef) {
      repoName = fullName(newRepoRef)
      repoProvider = newRepoRef.provider
    }
    // A repository named by URL goes as the URL (the server parses it); one
    // named by owner/name goes with its provider — and, on a daemon, as a
    // named clone. A listed repository not on a daemon that can clone by
    // name is one too, so the person's own token clones it.
    let gitUrl: string | undefined
    let clone: boolean | undefined
    if (freeTextMode && typedRef) {
      if (typedRef.url) {
        repoName = ''
        repoProvider = undefined
        gitUrl = typedRef.url
      } else {
        repoName = fullName(typedRef)
        repoProvider = typedRef.provider
      }
      clone = onDaemonRuntime || undefined
    } else if (listedCloneRef) {
      repoName = fullName(listedCloneRef)
      repoProvider = listedCloneRef.provider
      clone = true
    }
    await postSession({
      daemon_id: selectedDaemon?.id ?? '',
      repo: repoName || undefined,
      provider: repoProvider,
      git_url: gitUrl,
      clone,
      new_repo: newFolderMode || undefined,
      visibility: newRepoRef ? visibility : undefined,
    })
  }

  // POSTs the launch: where the repository comes from (repoFields) plus
  // everything else the sheet chose, the same for every kind of repo choice.
  async function postSession(repoFields: Record<string, unknown>) {
    const granted = kind === 'agent' ? effectiveLaunchPermissions(permissions, { runtime, boards: boards.state === 'ready' && boards.configured ? boards.boards : [], mcp: mcpAvailable ? mcp : [], clusterConfigured: !!clusterStatus?.configured, sandboxClusterAny: clusterStatus?.sandbox_cluster_children === 'any' }) : NO_LAUNCH_PERMISSIONS
    const launchPermissions = granted.children.length + granted.board_levels.length + granted.mcp_tools.length > 0 ? granted : undefined
    setSubmitting(true)
    setError(null)
    try {
      const res = await apiFetch('/api/sessions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          ...repoFields,
          title: name.trim() || defaultName,
          initial_prompt: initialPrompt,
          // Whatever the engine's picker selected: a full model id (for both
          // kinds — the CLI takes it as --model) and an effort only when the
          // model takes one and it isn't Auto. An engine with no model list
          // sends only a typed model name, if any — never a guessed one — and
          // otherwise runs on its account's own default.
          model: freeTextModelOffered ? (freeTextModel || undefined) : selectedModel?.id,
          effort: selectedEffort,
          dangerously_skip_permissions: dangerouslySkipPermissions,
          // Both are always explicit now (spec §2) — the server's "empty means
          // daemon" default exists only for callers older than this column.
          kind,
          runtime,
          engine: engine !== 'claude' ? engine : undefined,
          // Always said for an agent session, so the choice is the sheet's and
          // not a server default; fixed for the session's life.
          interaction: kind === 'agent' ? (unattended ? 'unattended' : 'interactive') : undefined,
          // Only when something is selected, and only where a grant can run at all.
          board_id: boardId || undefined,
          mcp: mcpAvailable && mcp.length > 0 ? mcp : undefined,
          permissions: launchPermissions,
        }),
      })
      if (!res.ok) {
        const text = await res.text()
        throw new Error(text || `HTTP ${res.status}`)
      }
      const json = await res.json() as { session_id: string; permission_problems?: unknown }
      if (!json.session_id) {
        throw new Error('server response missing session_id')
      }
      // The session started; a permission it could not be given is said once, and it asks instead.
      if (Array.isArray(json.permission_problems) && json.permission_problems.length > 0) {
        useToastStore.getState().addToast({ title: 'Some permissions were not given', body: json.permission_problems.filter(p => typeof p === 'string').join(' '), url: '/sessions/' + json.session_id })
      }
      addPendingSession(json.session_id)
      onClose()
      navigate('/sessions/' + json.session_id)
    } catch (err: unknown) {
      setError('Failed to launch session: ' + (err instanceof Error ? err.message : String(err)))
    } finally {
      setSubmitting(false)
    }
  }

  // The empty workspace's location belongs with its repository choice.
  const emptyWorkspacePlan = <>
    {/* What No repository will do, said before Launch — per
        runtime, since only the cluster is truly folder-less. */}
    {noRepoMode && !needsDaemon && (
      <div data-testid="no-repo-plan" style={createPlanStyle}>
        Uses an empty workspace that is deleted when the pod ends.
      </div>
    )}
    {noRepoMode && needsDaemon && selectedDaemon && (
      <div data-testid="no-repo-plan" style={createPlanStyle}>
        <div>
          Keeps your files in {selectedDaemon.repos_root}/{scratchFolder}.
          <details className="launch-help"><summary>About this folder</summary><p>This scratch folder stays after the session and is hidden from the repository list.</p></details>
        </div>
        {scratchRenaming ? (
          <label style={{ display: 'flex', alignItems: 'center', gap: 4, marginTop: 6 }}>
            <span>{SCRATCH_PREFIX}</span>
            <input className="settings-field"
              data-testid="scratch-name-input"
              aria-label="Scratch folder name"
              value={scratchSuffix}
              onChange={e => setScratchSuffix(e.target.value)}
              autoCorrect="off"
              autoCapitalize="none"
              autoComplete="off"
              spellCheck={false}
              style={{
                flex: 1,
                minWidth: 0,
                background: 'var(--scree)',
                border: `1px solid ${scratchNameInvalid ? 'var(--danger)' : 'var(--stone)'}`,
                borderRadius: 6,
                color: 'var(--chalk)',
                padding: '4px 8px',
                fontFamily: 'inherit',
                fontSize: 13,
              }}
            />
          </label>
        ) : (
          <button className="settings-action"
            type="button"
            data-testid="scratch-rename"
            onClick={() => setScratchRenaming(true)}
            style={{ marginTop: 4, background: 'none', border: 'none', padding: 0, color: 'inherit', textDecoration: 'underline', cursor: 'pointer', fontFamily: 'inherit', fontSize: 'inherit' }}
          >
            Rename
          </button>
        )}
      </div>
    )}
  </>

  return (
    <div
      className="launch-overlay"
      style={{ display: open ? 'flex' : 'none' }}
      {...backdrop}
    >
      <div className="launch-panel" role="dialog" aria-modal="true" aria-label="New Session">
        {/* Header */}
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
          <span style={{ color: 'var(--blaze)', fontWeight: 700, fontSize: '1rem', letterSpacing: '0.08em', textTransform: 'uppercase' }}>New Session</span>
          <button className="settings-action"
            onClick={onClose}
            style={{ background: 'none', border: 'none', padding: 0, color: 'var(--fog)', fontSize: '1.4rem', cursor: 'pointer', fontFamily: 'inherit' }}
          >
            ✕
          </button>
        </div>

        {/* Shown from the cache while its refresh is on its way. */}
        {showBody && refreshingParts.length > 0 && (
          <div data-testid="launch-refreshing" role="status" style={{ color: 'var(--fog-dim)', fontSize: '0.75rem', marginTop: -10, marginBottom: 10 }}>
            Refreshing {refreshingParts.map(p => PART_LABELS[p]).join(', ')}…
          </div>
        )}
        {!showBody ? (
          <div style={{ color: 'var(--fog)', padding: 32, textAlign: 'center' }}>Loading...</div>
        ) : (
          <>
          <div className="launch-body">
          {/* Column: Repository */}
          {/* data-phone-order mirrors the CSS `order` in the max-width:899px
              block, so the stacking order is assertable in jsdom (which
              applies no stylesheet). Keep the two in step. */}
          <div className="launch-col-repo" data-testid="launch-col-repo" data-phone-order="1">
            {/* Section 1: Repository */}
            <div className="launch-repo-section">
              <span style={sectionLabel}>Repository</span>
              {!workspaceRuntimeReady && (runtime === 'cluster' || selectedDaemon?.discovery_version === 1) && <p role="status" className="workspace-note">Workspace sessions and owner shells are not enabled for this runtime.</p>}
              {useWorkspacePicker && <WorkspacePicker key={`${workspaceScope}:${pickerReset}`} daemon={selectedDaemon} runtime={runtime} repos={repos}
                recentRepos={recentRepos} hideTree={repoActions || newFolderMode || freeTextMode}
                actions={<div className="launch-repo-actions">
                  <button type="button" className="settings-action btn small" aria-pressed={repoActions || newFolderMode || freeTextMode} onClick={() => {
                    setRepoActions(value => !value); setWorkspacePick({ scope: workspaceScope, selection: null, names: [] }); setPickerReset(value => value + 1)
                    setNoRepoMode(false); setSelectedRepo(null); setNewFolderMode(false); setFreeTextMode(false)
                    requestAnimationFrame(() => repoInputRef.current?.focus())
                  }} title="Create a folder or repository, or clone from a URL">New repository</button>
                  <button type="button" className="settings-action btn small" aria-pressed={noRepoMode} onClick={() => {
                    selectNoRepo(); setRepoActions(false); setWorkspacePick({scope: workspaceScope, selection: null, names: []}); setPickerReset(value => value + 1)
                  }}>Empty workspace</button>
                </div>}
                onChange={(selection, names) => { setWorkspacePick({scope: workspaceScope, selection, names}); setNoRepoMode(false); setSelectedRepo(null); setNewFolderMode(false); setFreeTextMode(false); setRepoActions(false) }} />}
              {useWorkspacePicker && emptyWorkspacePlan}
              <RepositoryActions modern={useWorkspacePicker} hidden={!repoActions && !newFolderMode && !freeTextMode}>
              <input className="settings-field"
                ref={repoInputRef}
                value={search}
                onChange={e => setSearch(e.target.value)}
                placeholder={newFolderMode ? (runtime === 'cluster' ? 'you/new-project' : 'Folder name...') : runtime === 'cluster' ? 'org/name' : 'Search repos, or owner/name or a URL...'}
                style={{
                  width: '100%',
                  background: 'var(--scree)',
                  border: '1px solid var(--stone)',
                  borderRadius: 8,
                  color: 'var(--chalk)',
                  padding: '8px 12px',
                  marginBottom: 8,
                  boxSizing: 'border-box',
                  fontFamily: 'inherit',
                }}
              />
              {/* Outside the scrolling list, right under the input it points
                  at, so it can't scroll out of view. */}
              {clusterRepoUnresolved && (
                <div
                  data-testid="cluster-repo-unresolved"
                  role="alert"
                  style={{ color: 'var(--amber)', fontSize: 13, marginBottom: 8 }}
                >
                  {CLUSTER_REPO_UNRESOLVED}
                  <button className="settings-action"
                    type="button"
                    onClick={() => repoInputRef.current?.focus()}
                    style={{ marginLeft: 6, background: 'none', border: 'none', padding: 0, color: 'inherit', textDecoration: 'underline', cursor: 'pointer', fontFamily: 'inherit', fontSize: 'inherit' }}
                  >
                    above ↑
                  </button>
                </div>
              )}
              {/* Nothing listed yet: say how a repository gets here, since the
                  list below is just No repository / New folder. */}
              {reposLoaded && repos.length === 0 && !newFolderMode && runtime !== 'cluster' && (
                <div
                  data-testid="repos-empty-hint"
                  style={{ color: 'var(--fog)', fontSize: '0.78rem', marginBottom: 8 }}
                >
                  Type owner/name or paste a Git URL to clone a repository, or connect GitHub/GitLab in {settingsLink} to list yours.
                </div>
              )}
              {reposStale && !reposStaleDismissed && (
                <div
                  data-testid="repos-stale-note"
                  role="status"
                  style={{
                    display: 'flex',
                    alignItems: 'flex-start',
                    gap: 8,
                    fontSize: '0.78rem',
                    color: 'var(--amber)',
                    background: 'var(--scree)',
                    border: '1px solid var(--stone)',
                    borderRadius: 6,
                    padding: '8px 10px',
                    marginBottom: 8,
                  }}
                >
                  <span style={{ flex: 1 }}>
                    Couldn't load your GitHub/GitLab repositories, so some may be missing below. Check your token
                    in {settingsLink} — it may be expired or revoked. You can still type owner/name or paste a URL.
                  </span>
                  <button className="settings-action"
                    type="button"
                    aria-label="Dismiss"
                    data-testid="repos-stale-dismiss"
                    onClick={() => setReposStaleDismissed(true)}
                    style={{ background: 'none', border: 'none', padding: 0, color: 'var(--fog)', cursor: 'pointer', fontFamily: 'inherit', fontSize: '0.9rem' }}
                  >
                    ✕
                  </button>
                </div>
              )}
              {/* The list scrolls inside a wrapper that takes whatever height the
                  modal gives the column (see LaunchSheet.css): fixed-height on a
                  phone, filling the row on desktop. */}
              <div className="launch-repo-list-wrap">
              <div className="launch-repo-list" data-testid="repo-list">
                {/* Always first and always there, whatever is typed above:
                    the default pick, a session tied to no repository. */}
                {!useWorkspacePicker && <div
                  data-testid="no-repo-option"
                  role="option"
                  aria-selected={noRepoMode}
                  style={noRepoMode ? itemSelected : itemBase}
                  onClick={selectNoRepo}
                >
                  <div style={{ fontWeight: 600, fontSize: '0.9rem' }}>{NO_REPO_LABEL}</div>
                  <div style={{ fontSize: '0.75rem', marginTop: 2, color: 'var(--fog)' }}>
                    {runtime === 'cluster' ? 'an empty, throwaway workspace' : 'a new scratch folder, nothing cloned'}
                  </div>
                </div>}
                {/* A repository typed as owner/name or pasted as a URL is
                    launchable even when nobody listed it or has it checked
                    out: the cluster pod clones it, and so does a daemon. */}
                {typedRepoUnmatched && (
                  <div
                    data-testid="free-text-repo"
                    style={freeTextMode ? itemSelected : { ...itemBase, borderStyle: 'dashed' }}
                    onClick={selectFreeTextRepo}
                  >
                    <div style={{ fontWeight: 600, fontSize: '0.9rem' }}>
                      Use {typedRef?.url ? fullName(typedRef) : folderName}
                    </div>
                    <div style={{ fontSize: '0.75rem', marginTop: 2, color: 'var(--fog)' }}>
                      {runtime === 'cluster'
                        ? 'clone this repository into the pod'
                        : `clone this repository onto ${selectedDaemon?.name ?? 'the daemon'}`}
                    </div>
                    {/* Which host it is on: a URL says so itself; a bare
                        owner/name is GitHub unless another is picked. */}
                    {typedRef && (typedRef.url ? (
                      <div data-testid="free-text-provider" style={{ fontSize: '0.75rem', marginTop: 4, color: 'var(--fog)' }}>
                        on {providerLabel(typedRef.provider)}
                      </div>
                    ) : (
                      <div role="radiogroup" aria-label="Git provider" style={{ display: 'flex', gap: 6, marginTop: 6 }}>
                        {Object.keys(PROVIDER_LABELS).map(p => (
                          <button className="settings-action"
                            key={p}
                            type="button"
                            role="radio"
                            aria-checked={typedRef.provider === p}
                            data-testid={`free-text-provider-${p}`}
                            onClick={e => { e.stopPropagation(); setFreeTextProvider(p); selectFreeTextRepo() }}
                            style={{
                              fontSize: '0.7rem',
                              padding: '2px 8px',
                              borderRadius: 4,
                              cursor: 'pointer',
                              fontFamily: 'inherit',
                              background: typedRef.provider === p ? 'var(--selected-bg)' : 'var(--basalt)',
                              color: typedRef.provider === p ? 'var(--selected-text)' : 'var(--fog)',
                              border: `1px solid ${typedRef.provider === p ? 'var(--selected-border)' : 'var(--stone)'}`,
                            }}
                          >
                            {PROVIDER_LABELS[p]}
                          </button>
                        ))}
                      </div>
                    ))}
                  </div>
                )}
                {/* New folder (daemon): an empty directory under the repos
                    root. New repository (cluster): a pod is thrown away when
                    the session ends, so the server creates the repository on
                    the person's git host first and the pod clones it. */}
                <div
                  data-testid="new-folder-option"
                  style={newFolderMode ? itemSelected : { ...itemBase, borderStyle: 'dashed' }}
                  onClick={selectNewFolder}
                >
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                    <span style={{ color: newFolderMode ? 'var(--selected-text)' : 'var(--fog)', fontSize: '0.85rem' }}>+</span>
                    <span style={{ fontWeight: 600, fontSize: '0.9rem' }}>
                      {runtime === 'cluster'
                        ? (newRepoRef ? `Create "${fullName(newRepoRef)}" on ${providerLabel(newRepoRef.provider)}` : 'New repository')
                        : (folderName !== '' ? `Create folder "${folderName}"` : 'New folder')}
                    </span>
                  </div>
                  <div style={{ fontSize: '0.75rem', marginTop: 2, color: 'var(--fog)' }}>
                    {runtime === 'cluster'
                      ? (newFolderMode && !newRepoRef ? 'type owner/name above ↑' : 'create it on GitHub or GitLab and start in it')
                      : (newFolderMode && folderName === '' ? 'type a name above ↑' : 'new empty directory')}
                  </div>
                  {runtime === 'cluster' && newFolderMode && (
                    <>
                      <div role="radiogroup" aria-label="Git host for the new repository" style={{ display: 'flex', gap: 6, marginTop: 6 }}>
                        {Object.keys(PROVIDER_LABELS).map(p => (
                          <button className="settings-action"
                            key={p}
                            type="button"
                            role="radio"
                            aria-checked={freeTextProvider === p}
                            data-testid={`new-repo-provider-${p}`}
                            onClick={e => { e.stopPropagation(); setFreeTextProvider(p) }}
                            style={{
                              fontSize: '0.7rem',
                              padding: '2px 8px',
                              borderRadius: 4,
                              cursor: 'pointer',
                              fontFamily: 'inherit',
                              background: freeTextProvider === p ? 'var(--selected-bg)' : 'var(--basalt)',
                              color: freeTextProvider === p ? 'var(--selected-text)' : 'var(--fog)',
                              border: `1px solid ${freeTextProvider === p ? 'var(--selected-border)' : 'var(--stone)'}`,
                            }}
                          >
                            {PROVIDER_LABELS[p]}
                          </button>
                        ))}
                      </div>
                      <div role="radiogroup" aria-label="Visibility" style={{ display: 'flex', gap: 6, marginTop: 6 }}>
                        {(['private', 'public'] as const).map(v => (
                          <button className="settings-action"
                            key={v}
                            type="button"
                            role="radio"
                            aria-checked={visibility === v}
                            data-testid={`new-repo-visibility-${v}`}
                            onClick={e => { e.stopPropagation(); setVisibility(v) }}
                            style={{
                              fontSize: '0.7rem',
                              padding: '2px 8px',
                              borderRadius: 4,
                              cursor: 'pointer',
                              fontFamily: 'inherit',
                              background: visibility === v ? 'var(--selected-bg)' : 'var(--basalt)',
                              color: visibility === v ? 'var(--selected-text)' : 'var(--fog)',
                              border: `1px solid ${visibility === v ? 'var(--selected-border)' : 'var(--stone)'}`,
                            }}
                          >
                            {v === 'private' ? 'Private' : 'Public'}
                          </button>
                        ))}
                      </div>
                      {newRepoTokenMissing && newRepoRef && (
                        <div data-testid="new-repo-token-note" style={{ fontSize: '0.75rem', marginTop: 6, color: 'var(--amber)' }}>
                          No {providerLabel(newRepoRef.provider)} token in {settingsLink} — the repository will exist only in the pod until it is pushed.
                        </div>
                      )}
                    </>
                  )}
                </div>
                {/* The list is still on its way (cold start): placeholders,
                    not an empty list that then jumps. */}
                {!reposLoaded && [0, 1, 2].map(i => (
                  <div
                    key={`skeleton-${i}`}
                    data-testid="repos-skeleton"
                    aria-hidden="true"
                    style={{ ...itemBase, cursor: 'default', height: 38, opacity: 0.45 - i * 0.12 }}
                  />
                ))}
                {(useWorkspacePicker ? [] : filteredRepos).map(repo => (
                  <div
                    key={repoKey(repo)}
                    style={selectedRepo && repoKey(selectedRepo) === repoKey(repo) ? itemSelected : itemBase}
                    onClick={() => selectRepo(repo)}
                  >
                    <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                      <span style={{ fontWeight: 600, fontSize: '0.9rem' }}>{repo.name}</span>
                      {repo.is_local && (
                        <span style={{
                          fontSize: '0.65rem',
                          color: 'var(--fog)',
                          border: '1px solid var(--stone)',
                          borderRadius: 4,
                          padding: '1px 5px',
                          letterSpacing: '0.05em',
                        }}>local</span>
                      )}
                      {/* GitHub is the long-standing default and goes unmarked;
                          any other provider is named, so two same-named repos
                          on different hosts can be told apart. */}
                      {repo.provider && repo.provider !== 'github' && (
                        <span data-testid="repo-provider" style={{
                          fontSize: '0.65rem',
                          color: 'var(--fog)',
                          border: '1px solid var(--stone)',
                          borderRadius: 4,
                          padding: '1px 5px',
                          letterSpacing: '0.05em',
                        }}>{PROVIDER_LABELS[repo.provider] ?? repo.provider}</span>
                      )}
                    </div>
                    {!repo.is_local && repo.full_name !== repo.name && (
                      <div data-testid="repo-full-name" style={{ fontSize: '0.75rem', marginTop: 2, color: 'var(--fog)' }}>
                        {repo.full_name}
                      </div>
                    )}
                    {repo.is_local && repo.remote && (
                      <div data-testid="repo-remote" style={{ fontSize: '0.75rem', marginTop: 2, color: 'var(--fog)' }}>
                        {repo.remote}
                      </div>
                    )}
                    <div style={{ fontSize: '0.75rem', marginTop: 2 }}>
                      {repo.checked_out.length > 0 ? (
                        <span style={{ color: 'var(--lichen)' }}>✓ on {repo.checked_out.join(', ')}</span>
                      ) : (
                        <span style={{ color: 'var(--fog)' }}>not checked out</span>
                      )}
                    </div>
                  </div>
                ))}
                {filteredRepos.length === 0 && search !== '' && !typedRepoUnmatched && (
                  <div style={{ color: 'var(--fog)', fontSize: '0.85rem', padding: '8px 0' }}>No existing repos match.</div>
                )}
              </div>
              </div>
              </RepositoryActions>
            </div>
          </div>

          {/* Column: session config */}
          <div className="launch-col-config" data-testid="launch-col-config" data-phone-order="3">
            {/* Section 4: Session name */}
            <div style={{ marginBottom: 14 }}>
              <span style={sectionLabel}>Session Name</span>
              <input className="settings-field"
                value={name}
                onChange={e => setTypedName(e.target.value)}
                placeholder={defaultName}
                data-testid="launch-session-name"
                aria-label="Session name"
                autoCorrect="off"
                autoCapitalize="none"
                autoComplete="off"
                style={{
                  width: '100%',
                  background: 'var(--scree)',
                  border: '1px solid var(--stone)',
                  borderRadius: 8,
                  color: 'var(--chalk)',
                  padding: '8px 12px',
                  boxSizing: 'border-box',
                  fontFamily: 'inherit',
                }}
              />
            </div>

            {/* Section 5: Initial prompt */}
            <div style={{ marginBottom: 14 }}>
              <span style={sectionLabel}>Initial Prompt</span>
              <textarea className="settings-field"
                value={initialPrompt}
                onChange={e => setInitialPrompt(e.target.value)}
                placeholder="What should the agent work on?"
                autoCorrect="off"
                autoCapitalize="none"
                autoComplete="off"
                spellCheck={false}
                style={{
                  width: '100%',
                  background: 'var(--scree)',
                  border: '1px solid var(--stone)',
                  borderRadius: 8,
                  color: 'var(--chalk)',
                  padding: '8px 12px',
                  resize: 'none',
                  height: 80,
                  fontFamily: 'system-ui',
                  boxSizing: 'border-box',
                }}
              />
            </div>

            {/* Interaction: whether anyone will be reading the chat. A
                terminal session has no chat, so the choice is not offered. */}
            {kind === 'agent' && (
              <label className="settings-field"
                data-testid="launch-unattended"
                style={{ display: 'flex', gap: 8, alignItems: 'flex-start', fontSize: '0.8rem', color: 'var(--chalk)', marginBottom: 14, cursor: 'pointer' }}
              >
                <input className="settings-toggle-control"
                  type="checkbox"
                  data-testid="unattended-toggle"
                  checked={unattended}
                  onChange={e => setUnattended(e.target.checked)}
                />
                <span>
                  Unattended
                  <span style={{ display: 'block', color: 'var(--fog)', fontSize: 12, marginTop: 2 }}>
                    Decide and finish without waiting for you.
                  </span>
                </span>
              </label>
            )}
            {kind === 'agent' && <details className="launch-help"><summary>About unattended sessions</summary><p>{UNATTENDED_HELP}</p></details>}

            {(boards.state !== 'ready' || boards.configured) && (
              <div style={{ marginBottom: 14 }}>
                {boards.state === 'ready' && boards.configured ? (
                  <>
                    <label htmlFor="launch-board" style={sectionLabel}>Board</label>
                    <select className="settings-field select" title="Adds tools for the selected board and makes the session private to you." id="launch-board" value={boardId} onChange={e => pickBoard(e.target.value)}
                      style={{ width: '100%' }}
                    >
                      <option value="">No board</option>
                      {boardId && !boards.boards.some(b => b.id === boardId) && <option value={boardId}>Previously selected board (unavailable)</option>}
                      {boards.boards.map(b => <option key={b.id} value={b.id}>{b.name}</option>)}
                    </select>
                    <div style={{ color: 'var(--fog)', fontSize: 12, marginTop: 6 }}>
                      Give this private session access to a board.
                    </div>
                  </>
                ) : boards.state === 'failed' ? (
                  <div role="status" style={{ color: 'var(--fog)', fontSize: 13 }}>
                    Couldn't load boards. You can launch without one.{' '}
                    <button type="button" className="settings-action btn small" onClick={boards.retry}>Retry boards</button>
                  </div>
                ) : <div role="status" style={{ color: 'var(--fog)', fontSize: 13 }}>Checking available boards…</div>}
              </div>
            )}
            {boardId && (boards.state !== 'ready' || !boards.configured) && (
              <button className="settings-action" type="button" onClick={() => pickBoard('')}>Clear board</button>
            )}

            {/* MCP servers: all unchecked, every tool off, until the person chooses */}
            <div style={{ marginBottom: 14 }} data-testid="launch-mcp">
              <span style={sectionLabel}>MCP Servers</span>
              {mcpAvailable ? (
                <>
                  <details className="launch-tools"><summary>Choose connections{mcp.length > 0 ? ` (${mcp.length} selected)` : ' (optional)'}</summary>
                    <p className="launch-help">Only the tools you enable are available through these connections.</p>
                    <McpPicker value={mcp} onChange={setMcp} connections={launchMcpConnections} />
                  </details>
                  {mcp.length > 0 && (
                    <div data-testid="launch-mcp-private" style={{ color: 'var(--fog)', fontSize: 12, marginTop: 6 }}>
                      A session with MCP servers is private to you.
                    </div>
                  )}
                </>
              ) : (
                <div data-testid="launch-mcp-unavailable" style={{ color: 'var(--fog)', fontSize: 13 }}>
                  Choose a supported sandbox to add MCP tools.
                  <details className="launch-help"><summary>MCP availability</summary><p>MCP servers are available for Claude and Codex agent sessions in a cluster pod or an updated local sandbox, not on this machine unsandboxed.</p></details>
                </div>
              )}
            </div>

            <LaunchPermissions kind={kind} runtime={runtime}
              boards={boards.state === 'ready' && boards.configured ? boards.boards : []}
              boardsLoading={boards.state === 'loading'}
              engines={onCluster ? [] : selectedDaemon?.available_engines ?? []}
              cluster={clusterStatus?.configured ? { engines: clusterStatus.available_engines ?? [], sandboxAny: clusterStatus.sandbox_cluster_children === 'any' } : undefined}
              mcp={mcpAvailable ? mcp : []}
              connectionName={id => launchMcpConnections.cached()?.find(c => c.id === id)?.name ?? id.slice(0, 8)}
              value={permissions} onChange={setPermissions} />
          </div>

          {/* Column: Run — every runtime/kind/engine choice, nothing hidden */}
          <RunColumn
            runtime={runtime}
            onRuntimeChange={pickRuntime}
            kind={kind}
            onKindChange={pickKind}
            engine={engine}
            onEngineChange={pickEngine}
            models={freeTextModelOffered ? [] : listModels}
            modelsLoading={engineModels.loading}
            modelsFallback={engineModels.fallback}
            modelsError={catalogTarget.runtime === 'cluster' ? engineModels.error : undefined}
            modelsReconnect={catalogTarget.runtime === 'cluster' && engineModels.reconnect}
            modelsRefreshing={catalogTarget.runtime === 'cluster' && engineModels.refreshing}
            onModelsRetry={engineModels.retry}
            model={selectedModel?.id ?? ''}
            onModelChange={pickModel}
            effort={selectedEffort}
            onEffortChange={pickEffort}
            modelNotice={[engineNotice?.engine === engine ? engineNotice.text : null, modelNotice].filter(Boolean).join('. ') || null}
            modelFreeText={freeTextModelOffered
              ? {
                  value: typedHere.model, onChange: typeModel, invalid: freeTextModelInvalid,
                  efforts: engineModels.engineEfforts, effort: freeTextEffort, onEffortChange: pickTypedEffort,
                }
              : undefined}
            daemons={daemons}
            selectedDaemon={selectedDaemon}
            onDaemonChange={d => { daemonPickedRef.current = true; setSelectedDaemon(d as DaemonApiInfo) }}
            onReposRootChanged={applyReposRoot}
            clusterConfigured={!!clusterStatus?.configured}
            availableEnginesHere={availableEnginesHere}
            engineChoices={launchOptions.engines}
            engineDiscoveryState={launchOptions.state}
            engineDiscoveryError={launchOptions.error}
            onEngineDiscoveryRetry={launchOptions.retry}
            unavailableEngines={launchOptions.unavailable}
            clusterEngineBlocked={clusterEngineBlocked}
            clusterGitMissing={clusterGitMissing}
            settingsLink={settingsLink}
            engineMissingNote={sandboxClaudeMissing && selectedDaemon ? (
              <span data-testid="sandbox-claude-missing">
                {selectedDaemon.name} has no Claude login or API key to give the Local sandbox, so this launch
                will be refused. Log in with <code>claude</code> on that machine, set CLAUDE_CODE_OAUTH_TOKEN or
                ANTHROPIC_API_KEY for its daemon, or pick another engine.
              </span>
            ) : undefined}
            dangerouslySkipPermissions={dangerouslySkipPermissions}
            onSkipPermissionsChange={setDangerouslySkipPermissions}
            daemonNotes={
              <>
                {cloneRef && cloneFolder && selectedDaemon && needsDaemon && (
                  <div data-testid="clone-plan" style={{
                    background: cloneFolder.present ? 'var(--basalt)' : 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
                    border: `1px solid ${cloneFolder.present ? 'var(--scree-2)' : 'var(--blaze)'}`,
                    borderRadius: 6,
                    padding: '8px 12px',
                    marginTop: 8,
                    color: cloneFolder.present ? 'var(--fog-dim)' : 'var(--amber)',
                    fontSize: 13,
                  }}>
                    {cloneFolder.present
                      ? `✓ Already on ${selectedDaemon.name} at ${selectedDaemon.repos_root}/${cloneFolder.folder}`
                      : `⬇ Will clone ${fullName(cloneRef)} to ${selectedDaemon.repos_root}/${cloneFolder.folder} before starting`}
                  </div>
                )}
                {cloneFolder?.displaced && selectedDaemon && needsDaemon && (
                  <div data-testid="clone-folder-collision" role="alert" style={{ color: 'var(--amber)', fontSize: 13, marginTop: 8 }}>
                    {selectedDaemon.repos_root}/{cloneFolder.displaced} on {selectedDaemon.name} is a different repository, so it is left alone.
                  </div>
                )}
                {cloneHostTokenWithheld && cloneRef && selectedDaemon && (
                  <p data-testid="clone-host-token-note" style={{
                    fontSize: '0.78rem',
                    color: 'var(--fog)',
                    background: 'var(--scree)',
                    border: '1px solid var(--stone)',
                    borderRadius: 6,
                    padding: '8px 10px',
                    margin: '8px 0 0',
                  }}>
                    On This machine your {providerLabel(cloneRef.provider)} token isn't used for the clone: any other
                    session on {selectedDaemon.name} could read it while it runs. A public repository clones as it is;
                    for a private one pick Local sandbox, where the clone runs inside the container.
                  </p>
                )}
                {cloneGitMissing && cloneRef && needsDaemon && (
                  <p data-testid="clone-git-advisory" style={{
                    fontSize: '0.78rem',
                    color: 'var(--fog)',
                    background: 'var(--scree)',
                    border: '1px solid var(--stone)',
                    borderRadius: 6,
                    padding: '8px 10px',
                    margin: '8px 0 0',
                  }}>
                    You have no {providerLabel(cloneRef.provider)} token. A public repository clones without one; a
                    private one needs yours in {settingsLink}, or this machine's own access to it.
                  </p>
                )}
                {!cloneRef && selectedRepo && selectedDaemon && needsDaemon && !isCheckedOut && !selectedRepo.is_local && (
                  <div style={{
                    background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
                    border: '1px solid var(--blaze)',
                    borderRadius: 6,
                    padding: '8px 12px',
                    marginTop: 8,
                    color: 'var(--amber)',
                    fontSize: 13,
                  }}>
                    ⬇ Will clone to {selectedDaemon.repos_root}/{selectedRepo.name} before starting
                  </div>
                )}
                {newFolderMode && selectedDaemon && needsDaemon && (
                  <div style={createPlanStyle}>
                    + Will create {selectedDaemon.repos_root}/{folderName}
                  </div>
                )}
                {!useWorkspacePicker && emptyWorkspacePlan}
              </>
            }
          />
          </div>
          <div className="launch-footer" data-testid="launch-footer">
          <div className="launch-footer-action" data-testid="launch-footer-action">
          {/* The acknowledgement sits in the footer, directly above the button it
              gates, so it is in view whenever it applies. Only where the session
              genuinely runs as you, with nothing between it and your filesystem. */}
          {runtime === 'daemon' && (
            <label style={{ display: 'flex', gap: 8, alignItems: 'flex-start', fontSize: '0.8rem', color: 'var(--amber)', marginBottom: 12 }}>
              <input
                type="checkbox"
                data-testid="agent-ack"
                checked={agentAck}
                onChange={e => setAgentAck(e.target.checked)}
              />
              {kind === 'agent' ? ACK_AGENT : ACK_TERMINAL}
            </label>
          )}

          {/* Submit */}
          <button className="settings-action"
            onClick={handleSubmit}
            disabled={!canSubmit}
            style={{
              width: '100%',
              background: canSubmit ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'var(--basalt)',
              color: canSubmit ? 'var(--amber)' : 'var(--fog-dim)',
              border: `1px solid ${canSubmit ? 'var(--blaze)' : 'var(--stone)'}`,
              borderRadius: 8,
              padding: '12px',
              fontWeight: 700,
              fontSize: '0.95rem',
              cursor: canSubmit ? 'pointer' : 'not-allowed',
              fontFamily: 'inherit',
              letterSpacing: '0.05em',
            }}
          >
            {noRepoMode || workspaceSelection ? 'Launch Session' : newFolderMode ? 'Create & Launch →' : isCheckedOut || selectedRepo?.is_local || cloneFolder?.present ? 'Launch Session' : 'Clone & Launch →'}
          </button>
          </div>

          {/* Say why, next to the button that is refusing: a disabled Launch
              with the reason three columns away reads as a broken sheet. On
              desktop it sits to the left of the button (the footer's left two
              columns); on a phone it stacks under it. */}
          <div className="launch-footer-note">
          {(blockedReason ?? (clusterRepoUnresolved ? CLUSTER_REPO_UNRESOLVED : null)) && (
            <div
              data-testid="launch-blocked-reason"
              style={{ color: 'var(--fog)', marginTop: 8, fontSize: 13 }}
            >
              {blockedReason ?? CLUSTER_REPO_UNRESOLVED}
            </div>
          )}

          {error && (
            <div style={{ color: 'var(--danger)', marginTop: 8, fontSize: 13 }}>{error}</div>
          )}
          </div>
          </div>
          </>
        )}
      </div>
    </div>
  )
}
