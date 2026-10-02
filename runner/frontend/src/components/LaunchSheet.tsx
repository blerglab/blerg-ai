import { useState, useEffect, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { randomSessionName } from '../sessionNames'
import { useSessionStore } from '../hooks/useSessionStore'
import { apiFetch } from '../apiFetch'
import { coreOrigin } from '../authClient'
import RunColumn from './RunColumn'
import McpPicker from './McpPicker'
import type { McpSelectionEntry } from '../types'
import { useBackdropClose } from '../hooks/useBackdropClose'
import { defaultRuntime, runtimeDisabledReason } from '../lib/runtimes'
import type { RunRuntime, RunKind, RunEngine } from '../lib/runtimes'
import { useEngineModels, defaultModel, effectiveEffort, isModelIdFor, isEffortToken, FREE_TEXT_MODEL_ENGINES } from '../lib/engineModels'
import { parseRepoInput, predictCloneFolder, fullName, providerLabel, PROVIDER_LABELS } from '../lib/repoRef'
import type { RepoRef } from '../lib/repoRef'
import { SCRATCH_PREFIX, NO_REPO_LABEL, newScratchSuffix, validScratchSuffix } from '../lib/noRepo'
import './LaunchSheet.css'

interface RepoInfo {
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

function repoKey(r: RepoInfo): string {
  return (r.is_local ? 'local:' + r.name + ':' : '') + (r.provider ?? '') + ':' + r.full_name
}

const CLUSTER_REPO_UNRESOLVED =
  'This folder has no GitHub or GitLab remote the cluster can clone — enter org/name'

interface DaemonApiInfo {
  id: string
  name: string
  mode: string
  repos_root: string
  status: string
  sandbox_available: boolean
  available_engines: string[]
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

interface ClusterStatusApi {
  configured: boolean
  available_engines?: string[]
  // Whether the operator's Secret carries a shared git token. A user with
  // their own GitHub credential doesn't need it; a user without one can only
  // clone public repositories when it is absent.
  git_configured?: boolean
}

// MyCredentials mirrors GET /api/me/credentials: which personal credentials
// the signed-in account has in blerg-core, presence only. `engines` never
// includes a git provider's kind ("github", "gitlab") — those are reported
// separately as `git` (any) and `git_providers` (which), because they gate
// cloning rather than engine choice. A response with `unavailable: true`
// (core unreachable/unconfigured), or a failed fetch, is stored as null =
// "unknown", which suppresses every hard block below.
interface MyCredentialsApi {
  engines: string[]
  git: boolean
  git_providers?: string[]
  unavailable?: boolean
}

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
  marginBottom: 8,
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

// The pre-launch "this will create…" note: a new folder, or No repository's
// scratch folder (or, on the cluster, its empty workspace).
const createPlanStyle: React.CSSProperties = {
  background: 'var(--basalt)',
  border: '1px solid var(--scree-2)',
  borderRadius: 6,
  padding: '8px 12px',
  marginTop: 8,
  color: 'var(--fog-dim)',
  fontSize: 13,
}

const itemSelected: React.CSSProperties = {
  ...itemBase,
  border: '1px solid var(--blaze)',
  background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
}

// Remembers the last-used Engine/Model across launches so people aren't
// re-picking every time — per-browser (localStorage), not synced across
// machines. Guarded: localStorage can throw in privacy-restricted contexts.
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

function loadDefault<T extends string>(key: string, valid: readonly T[], fallback: T): T {
  try {
    const v = localStorage.getItem(key)
    return v && (valid as readonly string[]).includes(v) ? (v as T) : fallback
  } catch {
    return fallback
  }
}

function saveDefault(key: string, value: string) {
  try {
    localStorage.setItem(key, value)
  } catch {
    // ignore — best-effort convenience only
  }
}

// hasStoredPreference distinguishes "never picked one" from "picked, but the
// stored value happened to be invalid" — loadDefault's fallback covers both,
// but only the former should be overridden by a daemon-suggested default.
// What the selected daemon can run on a daemon runtime, for the Engine
// picker's warning state; null = unknown (don't warn). In the Local sandbox,
// a daemon that reports sandbox_claude_credential answers for Claude outright
// (a host login is mounted; a token or API key in the daemon's environment is
// passed in), so there a missing available_engines — Go's empty list is null —
// reads as "no other engines". On This machine the list stays as reported:
// its Claude entry only means "a credentials file exists", and a keychain
// login or a CLAUDE_CODE_OAUTH_TOKEN in the service environment (the macOS
// install's usual setup) works there without one, so a null list must not
// turn into a warning.
function daemonEnginesHere(
  d: Pick<DaemonApiInfo, 'available_engines' | 'sandbox_claude_credential'> | null,
  runtime: RunRuntime,
  kind: RunKind,
): string[] | null {
  if (!d) return null
  // The credential-aware override only makes sense for an agent-kind
  // session: a Terminal session in the sandbox gets no Claude credential
  // either way (sandbox.go passes it none), so overriding on kind==='tmux'
  // would hide a real "claude isn't configured here" warning for it.
  if (runtime !== 'docker' || kind !== 'agent' || d.sandbox_claude_credential === undefined) {
    return d.available_engines ?? null
  }
  const others = (d.available_engines ?? []).filter(e => e !== 'claude')
  return d.sandbox_claude_credential ? ['claude', ...others] : others
}

function hasStoredPreference(key: string): boolean {
  try {
    return localStorage.getItem(key) !== null
  } catch {
    return false
  }
}

// engineOrder mirrors the daemon's own display order (see engines.go's
// engineOrder + the Claude-first convention) — used to pick the best engine
// out of a daemon's reported available_engines when suggesting a default.
const engineOrder = ['claude', 'codex', 'hermes', 'openclaw'] as const

// The engine ids this sheet will accept out of localStorage.
const ENGINE_IDS = engineOrder

export default function LaunchSheet({ open, onClose }: LaunchSheetProps) {
  const navigate = useNavigate()
  const addPendingSession = useSessionStore(s => s.addPendingSession)
  const backdrop = useBackdropClose(onClose)

  // The parent remounts this component (via a `key` tied to `open`) each time
  // the sheet opens, so all form state below starts fresh from its initial
  // value — no synchronous state resets in an effect (which the
  // react-hooks/set-state-in-effect rule disallows). `loading` therefore starts
  // true and is only cleared once the fetch settles.
  const [repos, setRepos] = useState<RepoInfo[]>([])
  const [daemons, setDaemons] = useState<DaemonApiInfo[]>([])
  const [loading, setLoading] = useState(true)
  const [search, setSearch] = useState('')
  const [selectedRepo, setSelectedRepo] = useState<RepoInfo | null>(null)
  // "No repository" is the default pick: a session tied to no repository.
  // On the cluster that is an empty, throwaway workspace; on a daemon it is a
  // new scratch folder under the repos root, named for the person
  // (scratchSuffix, renamable) so Launch needs nothing more.
  const [noRepoMode, setNoRepoMode] = useState(true)
  const [scratchSuffix, setScratchSuffix] = useState(() => newScratchSuffix())
  const [scratchRenaming, setScratchRenaming] = useState(false)
  const [newFolderMode, setNewFolderMode] = useState(false)
  // A repository nobody has checked out and nobody listed is still
  // launchable: a cluster pod clones it, and so does a daemon (into its
  // repos folder). freeTextMode is "use exactly what I typed" — owner/name,
  // or a pasted URL. freeTextProvider is the provider a bare owner/name is
  // read on; a URL names its own.
  const [freeTextMode, setFreeTextMode] = useState(false)
  const [freeTextProvider, setFreeTextProvider] = useState('github')
  const [selectedDaemon, setSelectedDaemon] = useState<DaemonApiInfo | null>(null)
  const [initialPrompt, setInitialPrompt] = useState('')
  // MCP servers and tools chosen for this session (McpPicker). Empty: none, and no `mcp` is sent.
  const [mcp, setMcp] = useState<McpSelectionEntry[]>([])
  // Session kind: the native agent harness (structured chat transcript, no
  // terminal) or the classic tmux-hosted terminal CLI. Agent is the default —
  // it is what this tool is for, and it is what the sandbox now covers.
  const [kind, setKind] = useState<RunKind>('agent')
  // Which CLI actually drives the session. Orthogonal to kind/runtime — either
  // engine can run as a terminal or agent session, on any runtime below.
  const [engine, setEngine] = useState<RunEngine>(() => loadDefault(LAST_ENGINE_KEY, ENGINE_IDS, 'claude'))
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
    Object.fromEntries(ENGINE_IDS.map(e => [e, loadStored(perEngineKey(LAST_MODEL_KEY, e), v => isModelIdFor(e, v))])))
  const [explicitEfforts, setExplicitEfforts] = useState<Record<string, string | null>>(() =>
    Object.fromEntries(ENGINE_IDS.map(e => [e, loadStored(perEngineKey(LAST_EFFORT_KEY, e), isEffortToken)])))
  const pickedModel = pickedModels[engine] ?? null
  const explicitEffort = explicitEfforts[engine] ?? null
  // Where a session actually runs, for both kinds (spec §2). The initial value
  // is only a placeholder: the real default lands from defaultRuntime() once
  // /api/daemons and /api/cluster/status answer, and never picks the
  // unsandboxed host when something safer is available.
  const [runtime, setRuntime] = useState<RunRuntime>('daemon')
  // The model list: GET /api/models/{engine}, asked about the selected daemon
  // (Codex's and Hermes's lists are what that daemon probed; Claude's ignores
  // it). The engine's built-in list shows until it answers, and if it fails.
  // A cluster session runs on no daemon, so it asks with none — and the hook
  // never uses a daemon-reported list asked for without a daemon. On a
  // daemon runtime it waits until a daemon is selected rather than ask
  // without one.
  const onCluster = runtime === 'cluster'
  const listDaemonId = onCluster ? undefined : selectedDaemon?.id
  const engineModels = useEngineModels(engine, listDaemonId, { enabled: onCluster || !!selectedDaemon })
  const listModels = engineModels.models
  const selectedModel = listModels.find(m => m.id === pickedModel) ?? defaultModel(engine, listModels)
  // No list for an engine whose CLI takes a model name (an older daemon, a
  // probe that found nothing, the cluster): an optional free-text name,
  // checked against the engine's model rule before it can be sent, and — when
  // the engine takes effort — an effort from its allowlist, Auto by default.
  // Its own state, never the list pick's, and it starts empty again whenever
  // the engine or where it runs changes.
  const freeTextModelOffered = listModels.length === 0 && !engineModels.loading && FREE_TEXT_MODEL_ENGINES.has(engine)
  const typedKey = `${engine}\n${runtime}\n${listDaemonId ?? ''}`
  const [typed, setTyped] = useState<{ key: string, model: string, effort: string }>({ key: typedKey, model: '', effort: '' })
  // Engine, runtime or daemon changed: start the typed name over (React's
  // adjust-state-while-rendering pattern — no effect, no stale frame).
  if (typed.key !== typedKey) setTyped({ key: typedKey, model: '', effort: '' })
  const typedHere = typed.key === typedKey ? typed : { key: typedKey, model: '', effort: '' }
  const freeTextModel = freeTextModelOffered ? typedHere.model.trim() : ''
  const freeTextModelInvalid = freeTextModel !== '' && !isModelIdFor(engine, freeTextModel)
  const freeTextEffort = freeTextModelOffered && engineModels.engineEfforts.includes(typedHere.effort) ? typedHere.effort : undefined
  const selectedEffort = freeTextModelOffered ? freeTextEffort : effectiveEffort(selectedModel, explicitEffort)
  // Once someone picks a runtime, no suggested default ever moves it again for
  // the life of this sheet. A ref, not state: nothing renders from it, and it
  // is only read inside event handlers/effects.
  const runtimePickedRef = useRef(false)
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
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [autoName] = useState(() => randomSessionName())
  const [name, setName] = useState('')
  const [clusterStatus, setClusterStatus] = useState<ClusterStatusApi | null>(null)
  const [myCreds, setMyCreds] = useState<MyCredentialsApi | null>(null)
  // GET /api/repos answered with stale_at: a GitHub/GitLab list (usually the
  // person's own, from their token) failed to refresh — typically a revoked
  // or mistyped token. Without saying so the picker just looks empty.
  const [reposStale, setReposStale] = useState(false)
  const [reposStaleDismissed, setReposStaleDismissed] = useState(false)
  // The list has arrived (possibly empty): the empty-state hint waits for it
  // so it doesn't flash while the first fetch is still in flight.
  const [reposLoaded, setReposLoaded] = useState(false)

  useEffect(() => {
    if (!open) return

    Promise.all([
      apiFetch('/api/repos').then(r => r.json()),
      apiFetch('/api/daemons').then(r => r.json()),
      apiFetch('/api/cluster/status').then(r => r.json()),
      // Personal credentials are advisory: a failure here must never fail the
      // whole sheet, so it resolves to null ("unknown") instead of rejecting.
      apiFetch('/api/me/credentials')
        .then(r => r.json())
        .then((c: MyCredentialsApi) => (c && !c.unavailable ? c : null))
        .catch(() => null),
    ])
      .then(([repoData, daemonData, clusterData, credsData]) => {
        const daemonList = daemonData as DaemonApiInfo[]
        setRepos(repoData.repos ?? [])
        setReposLoaded(true)
        setReposStale(!!repoData.stale_at)
        setDaemons(daemonList)
        setClusterStatus(clusterData)
        setMyCreds(credsData)
        // Suggest an engine default from what the first daemon actually has
        // configured — but only when nobody has picked one on this browser
        // yet. Once a person picks (or the picker's own saveDefault call
        // fires), their choice always wins over this one-time suggestion.
        // Resolved before the runtime default below, which depends on it.
        let effectiveEngine = loadDefault(LAST_ENGINE_KEY, ENGINE_IDS, 'claude')
        if (!hasStoredPreference(LAST_ENGINE_KEY)) {
          const available: string[] = daemonList[0]?.available_engines ?? []
          const suggestion = engineOrder.find(id => available.includes(id))
          if (suggestion) {
            setEngine(suggestion)
            effectiveEngine = suggestion
          }
        }
        // The runtime default depends on data that only just arrived, so it is
        // applied here — and only when nobody has picked one yet, so a refresh
        // can never move a choice out from under someone (spec §1). OpenClaw
        // runs only on the host, so a remembered OpenClaw lands on This
        // machine rather than on a card the sheet then disables — pickEngine
        // does the same when the choice is made here instead of remembered.
        const nextRuntime: RunRuntime | null = runtimePickedRef.current
          ? null
          : effectiveEngine === 'openclaw'
            ? 'daemon'
            : defaultRuntime(daemonList, !!(clusterData as ClusterStatusApi)?.configured)
        if (nextRuntime) setRuntime(nextRuntime)
        // Pre-select a daemon so the Run column can speak concretely about it
        // (sandbox image, engine availability) before a repo is chosen — and
        // when the default is the sandbox, one that actually has the image, so
        // the sheet does not open on its own "no sandbox image" warning.
        const preferred = nextRuntime === 'docker'
          ? (daemonList.find(d => d.sandbox_available) ?? daemonList[0])
          : daemonList[0]
        if (preferred) setSelectedDaemon(preferred)
      })
      .catch(err => {
        setError('Failed to load data: ' + err.message)
      })
      .finally(() => setLoading(false))
  }, [open])

  // A daemon's repos folder was changed from the Run column: every "will
  // clone to …" hint must now name the new folder, and the repo list must be
  // what is on disk there — the server already has it from the daemon.
  // Changes in quick succession each refetch; only the latest answer counts,
  // so a slow earlier one can't put back the previous folder's list.
  const reposRefetch = useRef(0)
  const applyReposRoot = (daemonId: string, reposRoot: string) => {
    setDaemons(ds => ds.map(d => (d.id === daemonId ? { ...d, repos_root: reposRoot } : d)))
    setSelectedDaemon(d => (d && d.id === daemonId ? { ...d, repos_root: reposRoot } : d))
    const seq = ++reposRefetch.current
    apiFetch('/api/repos')
      .then(r => r.json())
      .then(data => {
        if (seq !== reposRefetch.current) return
        setRepos(data.repos ?? [])
        setReposStale(!!data.stale_at)
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
  if (runtime === 'cluster' && newFolderMode) setNewFolderMode(false)

  // Engine availability for the picker's warning state: on the desktop
  // daemon, whatever the selected daemon reported; on cluster runtime,
  // whatever GET /api/cluster/status reported (OpenClaw never appears there
  // — it isn't wired into cluster sessions at all, see CLUSTER-RUNTIME.md).
  // null means "unknown" (still loading, or the source reported nothing) —
  // treated as "don't warn" rather than "unavailable", since a false warning
  // is worse than a missed one.
  // On cluster runtime an engine is runnable if EITHER the operator Secret
  // carries its credential (available_engines) OR this person has their own
  // (myCreds.engines) — a cluster session runs with the launching user's
  // credential when there is one.
  const availableEnginesHere: string[] | null =
    runtime === 'cluster'
      ? (clusterStatus
          ? Array.from(new Set([...(clusterStatus.available_engines ?? []), ...(myCreds?.engines ?? [])]))
          : null)
      : daemonEnginesHere(selectedDaemon, runtime, kind)

  const engineMissingHere = availableEnginesHere !== null && !availableEnginesHere.includes(engine)
  // A hard block is only honest when we actually know what this person has.
  // With myCreds unknown, the softer advisory warning stands instead.
  const clusterEngineBlocked = runtime === 'cluster' && myCreds !== null && engineMissingHere
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
  // What the Repository box says, read as a repository: owner/name (on the
  // picked provider) or a pasted URL (on its host's). null for anything else.
  const typedRef: RepoRef | null = parseRepoInput(folderName, freeTextProvider)
  const onDaemonRuntime = runtime !== 'cluster'

  const filteredRepos = repos.filter(r =>
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

  const hasRepoChoice =
    (noRepoMode && !scratchNameInvalid) ||
    selectedRepo !== null ||
    (newFolderMode && folderName !== '') ||
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
  const blockedReason = runtimeBlockedReason ?? hostClaudeBlockedReason ?? cloneByNameUnsupported ?? clonePlanProblem ?? scratchNameProblem

  const canSubmit = hasRepoChoice &&
    (!needsDaemon || selectedDaemon !== null) && !submitting &&
    (runtime !== 'daemon' || agentAck) && !clusterEngineBlocked && blockedReason === null &&
    !clusterRepoUnresolved && !freeTextModelInvalid

  // A grant is refused on the bare host and for any engine but Claude, and only an agent
  // session can carry one (the gateway is reached through Claude's MCP config).
  const mcpAvailable = (runtime === 'cluster' || runtime === 'docker') && engine === 'claude' && kind === 'agent'

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
    })
  }

  // POSTs the launch: where the repository comes from (repoFields) plus
  // everything else the sheet chose, the same for every kind of repo choice.
  async function postSession(repoFields: Record<string, unknown>) {
    setSubmitting(true)
    setError(null)
    try {
      const res = await apiFetch('/api/sessions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          ...repoFields,
          title: name.trim() || autoName,
          initial_prompt: initialPrompt,
          // Whatever the engine's picker selected: a full model id (for both
          // kinds — the CLI takes it as --model) and an effort only when the
          // model takes one and it isn't Auto. An engine with no model list
          // sends only a typed model name, if any — never a guessed one — and
          // otherwise runs on its account's own default.
          model: selectedModel?.id ?? (freeTextModel || undefined),
          effort: selectedEffort,
          dangerously_skip_permissions: dangerouslySkipPermissions,
          // Both are always explicit now (spec §2) — the server's "empty means
          // daemon" default exists only for callers older than this column.
          kind,
          runtime,
          engine: engine !== 'claude' ? engine : undefined,
          // Only when something is selected, and only where a grant can run at all.
          mcp: mcpAvailable && mcp.length > 0 ? mcp : undefined,
        }),
      })
      if (!res.ok) {
        const text = await res.text()
        throw new Error(text || `HTTP ${res.status}`)
      }
      const json = await res.json() as { session_id: string }
      if (!json.session_id) {
        throw new Error('server response missing session_id')
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

  return (
    <div
      className="launch-overlay"
      style={{ display: open ? 'flex' : 'none' }}
      {...backdrop}
    >
      <div className="launch-panel">
        {/* Header */}
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
          <span style={{ color: 'var(--blaze)', fontWeight: 700, fontSize: '1rem', letterSpacing: '0.08em', textTransform: 'uppercase' }}>New Session</span>
          <button
            onClick={onClose}
            style={{ background: 'none', border: 'none', color: 'var(--fog)', fontSize: '1.4rem', cursor: 'pointer', fontFamily: 'inherit' }}
          >
            ✕
          </button>
        </div>

        {loading ? (
          <div style={{ color: 'var(--fog)', padding: 32, textAlign: 'center' }}>Loading...</div>
        ) : (
          <div className="launch-body">
          {/* Column: Repository */}
          {/* data-phone-order mirrors the CSS `order` in the max-width:899px
              block, so the stacking order is assertable in jsdom (which
              applies no stylesheet). Keep the two in step. */}
          <div className="launch-col-repo" data-testid="launch-col-repo" data-phone-order="1">
            {/* Section 1: Repository */}
            <div className="launch-repo-section">
              <span style={sectionLabel}>Repository</span>
              <input
                ref={repoInputRef}
                value={search}
                onChange={e => setSearch(e.target.value)}
                placeholder={newFolderMode ? 'Folder name...' : runtime === 'cluster' ? 'org/name' : 'Search repos, or owner/name or a URL...'}
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
                  <button
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
                  <button
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
                <div
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
                </div>
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
                          <button
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
                              background: typedRef.provider === p ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'var(--basalt)',
                              color: typedRef.provider === p ? 'var(--amber)' : 'var(--fog)',
                              border: `1px solid ${typedRef.provider === p ? 'var(--blaze)' : 'var(--stone)'}`,
                            }}
                          >
                            {PROVIDER_LABELS[p]}
                          </button>
                        ))}
                      </div>
                    ))}
                  </div>
                )}
                {/* A cluster pod is created from a clone and thrown away when
                    the session ends — an empty directory in it has nowhere to
                    live and nothing to push to, so the option isn't offered. */}
                {runtime !== 'cluster' && (
                  <div
                    data-testid="new-folder-option"
                    style={newFolderMode ? itemSelected : { ...itemBase, borderStyle: 'dashed' }}
                    onClick={selectNewFolder}
                  >
                    <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                      <span style={{ color: newFolderMode ? 'var(--amber)' : 'var(--fog)', fontSize: '0.85rem' }}>+</span>
                      <span style={{ fontWeight: 600, fontSize: '0.9rem' }}>
                        {folderName !== '' ? `Create folder "${folderName}"` : 'New folder'}
                      </span>
                    </div>
                    <div style={{ fontSize: '0.75rem', marginTop: 2, color: 'var(--fog)' }}>
                      {newFolderMode && folderName === '' ? 'type a name above ↑' : 'new empty directory'}
                    </div>
                  </div>
                )}
                {filteredRepos.map(repo => (
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
            </div>
          </div>

          {/* Column: session config */}
          <div className="launch-col-config" data-testid="launch-col-config" data-phone-order="3">
            {/* Section 4: Session name */}
            <div style={{ marginBottom: 20 }}>
              <span style={sectionLabel}>Session Name</span>
              <input
                value={name}
                onChange={e => setName(e.target.value)}
                placeholder={autoName}
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
            <div style={{ marginBottom: 20 }}>
              <span style={sectionLabel}>Initial Prompt</span>
              <textarea
                value={initialPrompt}
                onChange={e => setInitialPrompt(e.target.value)}
                placeholder="What should Claude work on?"
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

            {/* MCP servers: all unchecked, every tool off, until the person chooses */}
            <div style={{ marginBottom: 20 }} data-testid="launch-mcp">
              <span style={sectionLabel}>MCP Servers</span>
              {mcpAvailable ? (
                <McpPicker value={mcp} onChange={setMcp} />
              ) : (
                <div data-testid="launch-mcp-unavailable" style={{ color: 'var(--fog)', fontSize: 13 }}>
                  MCP servers are available for Claude agent sessions in a cluster pod or the local sandbox, not on this machine unsandboxed, not for other engines, and not for terminal sessions.
                </div>
              )}
            </div>

            {/* Submit */}
            <button
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
              {noRepoMode ? 'Launch Session' : newFolderMode ? 'Create & Launch →' : isCheckedOut || selectedRepo?.is_local || cloneFolder?.present ? 'Launch Session' : 'Clone & Launch →'}
            </button>

            {/* Say why, next to the button that is refusing: a disabled Launch
                with the reason three columns away reads as a broken sheet. */}
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

          {/* Column: Run — every runtime/kind/engine choice, nothing hidden */}
          <RunColumn
            runtime={runtime}
            onRuntimeChange={pickRuntime}
            kind={kind}
            onKindChange={pickKind}
            engine={engine}
            onEngineChange={pickEngine}
            models={listModels}
            modelsLoading={engineModels.loading}
            modelsFallback={engineModels.fallback}
            model={selectedModel?.id ?? ''}
            onModelChange={pickModel}
            effort={selectedEffort}
            onEffortChange={pickEffort}
            modelFreeText={freeTextModelOffered
              ? {
                  value: typedHere.model, onChange: typeModel, invalid: freeTextModelInvalid,
                  efforts: engineModels.engineEfforts, effort: freeTextEffort, onEffortChange: pickTypedEffort,
                }
              : undefined}
            daemons={daemons}
            selectedDaemon={selectedDaemon}
            onDaemonChange={d => setSelectedDaemon(d as DaemonApiInfo)}
            onReposRootChanged={applyReposRoot}
            clusterConfigured={!!clusterStatus?.configured}
            availableEnginesHere={availableEnginesHere}
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
            ack={agentAck}
            onAckChange={setAgentAck}
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
                {/* What No repository will do, said before Launch — per
                    runtime, since only the cluster is truly folder-less. */}
                {noRepoMode && !needsDaemon && (
                  <div data-testid="no-repo-plan" style={createPlanStyle}>
                    Runs in an empty, throwaway workspace — nothing is kept after the session's pod ends.
                  </div>
                )}
                {noRepoMode && needsDaemon && selectedDaemon && (
                  <div data-testid="no-repo-plan" style={createPlanStyle}>
                    <div>
                      + Will create a scratch folder named {scratchFolder} in {selectedDaemon.repos_root} (kept
                      after the session; never listed as a repository)
                    </div>
                    {scratchRenaming ? (
                      <label style={{ display: 'flex', alignItems: 'center', gap: 4, marginTop: 6 }}>
                        <span>{SCRATCH_PREFIX}</span>
                        <input
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
                      <button
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
            }
          />
          </div>
        )}
      </div>
    </div>
  )
}
