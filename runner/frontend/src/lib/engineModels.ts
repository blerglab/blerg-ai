// Model pickers, for any engine: GET /api/models/{engine} (the server's
// source for that engine — Claude Code's own /model catalog for Claude; for
// Codex and Hermes, what the selected daemon probed from the engine itself,
// source "daemon"; an engine with no source answers an empty list), a
// per-engine built-in fallback for when the fetch fails, and the small rules
// the launch sheet and the in-session switcher share.
//
// Nothing here needs to change for a new engine: once the server has a list
// for it, its list renders and its model/effort are sent. Optional extras are
// data: an offline fallback in BUILTIN_MODELS, a tighter id rule in
// MODEL_ID_PATTERNS (the server's rule is the one enforced), a preferred
// default in DEFAULT_MODEL_PREFIX, a free-text model box for when no list
// was reported in FREE_TEXT_MODEL_ENGINES.
import { useEffect, useRef, useState } from 'react'
import { apiFetch } from '../apiFetch'
import type { EngineModel, ModelList } from '../types'

// ─── Per-engine data ─────────────────────────────────────────────────────────

const CLAUDE_EFFORTS = ['low', 'medium', 'high', 'xhigh', 'max']
const CLAUDE_EFFORTS_NO_XHIGH = ['low', 'medium', 'high', 'max']

// The offline fallback per engine. Claude's mirrors the server's
// models.Builtin() — the catalog as of this build.
export const BUILTIN_MODELS: Readonly<Record<string, readonly EngineModel[]>> = {
  claude: [
    { id: 'claude-opus-5-5', name: 'Opus 5.5', description: 'Most capable for ambitious work', section: 'main', efforts: CLAUDE_EFFORTS, default_effort: 'medium' },
    { id: 'claude-sonnet-5', name: 'Sonnet 5', description: 'Most efficient for everyday tasks', section: 'main', efforts: CLAUDE_EFFORTS, default_effort: 'high' },
    { id: 'claude-fable-5-1', name: 'Fable 5.1', description: 'For your toughest challenges', section: 'main', efforts: CLAUDE_EFFORTS, default_effort: 'high' },
    { id: 'claude-haiku-4-5-20251001', name: 'Haiku 4.5', description: 'Fastest for quick answers', section: 'main', efforts: [] },
    { id: 'claude-opus-5', name: 'Opus 5', description: '', section: 'overflow', efforts: CLAUDE_EFFORTS, default_effort: 'high' },
    { id: 'claude-fable-5', name: 'Fable 5', description: '', section: 'overflow', efforts: CLAUDE_EFFORTS, default_effort: 'high' },
    { id: 'claude-opus-4-8', name: 'Opus 4.8', description: '', section: 'overflow', efforts: CLAUDE_EFFORTS, default_effort: 'high' },
    { id: 'claude-opus-4-7', name: 'Opus 4.7', description: '', section: 'overflow', efforts: CLAUDE_EFFORTS, default_effort: 'xhigh' },
    { id: 'claude-opus-4-6', name: 'Opus 4.6', description: '', section: 'overflow', efforts: CLAUDE_EFFORTS_NO_XHIGH, default_effort: 'high' },
    { id: 'claude-sonnet-4-6', name: 'Sonnet 4.6', description: '', section: 'overflow', efforts: CLAUDE_EFFORTS_NO_XHIGH, default_effort: 'high' },
  ],
}

// The id shape a model from the server must have, per engine — the same rules
// the server applies. Engines not listed get the generic provider/model rule.
const MODEL_ID_PATTERNS: Readonly<Record<string, RegExp>> = {
  claude: /^claude-[a-z0-9.-]{1,64}$/,
}
const GENERIC_MODEL_ID = /^[A-Za-z0-9][A-Za-z0-9._:/@\-[\]]{0,127}$/

// Which model an engine defaults to: the first whose id starts with this
// prefix (Claude: the Sonnet family), else the first main model.
const DEFAULT_MODEL_PREFIX: Readonly<Record<string, string>> = {
  claude: 'claude-sonnet',
}

// Engines whose CLI takes a model name, so that when no list was reported
// (an older daemon, a probe that found nothing, the cluster runtime) the
// sheet offers a free-text "Model name (optional)" box instead of nothing.
// There is deliberately no fallback LIST for them: a stale name is worse
// than none.
export const FREE_TEXT_MODEL_ENGINES: ReadonlySet<string> = new Set(['codex', 'hermes'])

// An effort is a short lowercase token; which tokens an engine accepts is the
// server's business (per-engine allowlist) — this only keeps junk out.
const EFFORT_TOKEN = /^[a-z][a-z0-9_-]{0,31}$/

// The spawn APIs call the default engine ""; everything here calls it claude.
export function engineKey(engine: string | undefined): string {
  return engine || 'claude'
}

export function builtinModels(engine: string): readonly EngineModel[] {
  return BUILTIN_MODELS[engineKey(engine)] ?? []
}

export function isModelIdFor(engine: string, v: unknown): v is string {
  return typeof v === 'string' && (MODEL_ID_PATTERNS[engineKey(engine)] ?? GENERIC_MODEL_ID).test(v)
}

export function isEffortToken(v: unknown): v is string {
  return typeof v === 'string' && EFFORT_TOKEN.test(v)
}

// ─── Fetch + parse ───────────────────────────────────────────────────────────

// parseModelList re-checks the server's answer rather than trusting its shape:
// anything malformed is dropped. An empty list is a valid answer (the engine
// has no picker); a missing/garbled one is an error (callers fall back).
export function parseModelList(engine: string, data: unknown): EngineModel[] {
  const raw = (data as { models?: unknown })?.models
  if (!Array.isArray(raw)) throw new Error('model list: no models array')
  const out: EngineModel[] = []
  for (const m of raw) {
    if (!m || typeof m !== 'object') continue
    const r = m as Record<string, unknown>
    if (!isModelIdFor(engine, r.id) || out.some(o => o.id === r.id)) continue
    const efforts = Array.isArray(r.efforts)
      ? r.efforts.filter((e, i, a): e is string => isEffortToken(e) && a.indexOf(e) === i)
      : []
    out.push({
      id: r.id,
      name: typeof r.name === 'string' && r.name ? r.name : r.id,
      description: typeof r.description === 'string' ? r.description : '',
      section: r.section === 'main' ? 'main' : 'overflow',
      efforts,
      default_effort: typeof r.default_effort === 'string' && efforts.includes(r.default_effort) ? r.default_effort : undefined,
      effort_kind: isEffortToken(r.effort_kind) ? r.effort_kind : undefined,
    })
  }
  if (raw.length > 0 && out.length === 0) throw new Error('model list: no usable models')
  // Main first, each section in server order.
  return [...out.filter(m => m.section === 'main'), ...out.filter(m => m.section === 'overflow')]
}

const SOURCES: readonly ModelList['source'][] = ['live', 'builtin', 'daemon', 'none']

// parseModelSource reads the answer's `source`; anything unrecognised is
// treated as "none".
export function parseModelSource(data: unknown): ModelList['source'] {
  const s = (data as { source?: unknown })?.source
  return SOURCES.includes(s as ModelList['source']) ? (s as ModelList['source']) : 'none'
}

// fetchModelList asks for engine's list; daemonId names the daemon the session
// would run on (a daemon-reported list is that daemon's own).
export interface FetchedModelList {
  refreshing?: boolean
  error?: string
  models: EngineModel[]
  source: ModelList['source']
  // The engine's whole effort allowlist (for a model typed by hand).
  engineEfforts: string[]
}

export type ModelRuntime = 'cluster'

// Every picker identifies where the session runs. The server's model-source
// registry chooses an account-bound cluster provider or a public catalog;
// workstation inventories are scoped to the selected daemon.
export function modelCatalogTarget(runtime?: string, daemonId?: string): { daemonId?: string; runtime?: ModelRuntime } {
  return runtime === 'cluster' ? { runtime: 'cluster' } : { daemonId: daemonId || undefined }
}

export async function fetchModelList(engine: string, daemonId?: string, runtime?: ModelRuntime, forceRefresh = false): Promise<FetchedModelList> {
  const controller = new AbortController()
  let timer: ReturnType<typeof setTimeout> | undefined
  const deadline = new Promise<never>((_, reject) => {
    timer = setTimeout(() => {
      reject(new Error('Loading models timed out. You can use the default model or retry.'))
      controller.abort()
    }, 10_000)
  })
  try {
    // The deadline also bounds a stalled auth renewal or response body, even
    // when the underlying transport does not honor abort.
    return await Promise.race([requestModelList(engine, daemonId, runtime, forceRefresh, controller.signal), deadline])
  } finally {
    clearTimeout(timer)
  }
}

async function requestModelList(engine: string, daemonId?: string, runtime?: ModelRuntime, forceRefresh = false, signal?: AbortSignal): Promise<FetchedModelList> {
  const qs = runtime === 'cluster' ? `?runtime=cluster${forceRefresh ? '&refresh=1' : ''}` : daemonId ? `?daemon_id=${encodeURIComponent(daemonId)}` : ''
  const res = await apiFetch(`/api/models/${encodeURIComponent(engineKey(engine))}${qs}`, signal ? { signal } : undefined)
  if (!res.ok) {
    const data = runtime === 'cluster' ? await res.json().catch(() => null) : null
    throw new Error(typeof data?.error === 'string' ? data.error : `Could not load models (HTTP ${res.status}).`)
  }
  const data = await res.json()
  const rawEfforts = (data as { engine_efforts?: unknown })?.engine_efforts
  const engineEfforts = Array.isArray(rawEfforts)
    ? rawEfforts.filter((e, i, a): e is string => isEffortToken(e) && a.indexOf(e) === i)
    : []
  return {
    models: parseModelList(engine, data), source: parseModelSource(data), engineEfforts,
    ...(runtime === 'cluster' ? { refreshing: data.refreshing === true, error: typeof data.error === 'string' ? data.error : undefined } : {}),
  }
}

export async function fetchModels(engine: string, daemonId?: string): Promise<EngineModel[]> {
  return (await fetchModelList(engine, daemonId)).models
}

export interface EngineModelsState {
  refreshing?: boolean
  retry?: () => void
  error?: string
  models: readonly EngineModel[]
  loading: boolean
  // true when the fetch failed and the built-in list (possibly empty) is shown.
  fallback: boolean
  // Where the list came from, once the server answered.
  source?: ModelList['source']
  // The engine's effort allowlist, once the server answered ([] until then).
  engineEfforts: readonly string[]
}

// A place to keep each answer (per engine and daemon) between mounts, so a
// picker can show the last list at once while asking again underneath.
export interface ModelListCache {
  // Discard an in-flight answer if the signed-in account changed.
  identity?(): string
  get(engine: string, daemonId?: string, runtime?: ModelRuntime): FetchedModelList | undefined
  put(engine: string, daemonId: string | undefined, value: FetchedModelList, runtime?: ModelRuntime): void
}

export interface UseEngineModelsOptions {
  // A cluster catalog is independent of every workstation catalog.
  runtime?: ModelRuntime
  // false = don't ask yet (e.g. the daemon the session will run on is still
  // loading): the built-in list shows, still "loading".
  enabled?: boolean
  // When given (a stable object): the last answer for this engine and daemon
  // shows at once, not loading, while the server is asked again; each answer
  // is put back; a failed ask keeps the cached list rather than falling back.
  cache?: ModelListCache
}

// toState reads one answer for daemonId. A daemon-reported list (source
// "daemon") is per workstation, so it is only ever used when daemonId named
// the daemon: asked without one, the server would answer with whichever
// daemon reported (or a union of them), which says nothing about where this
// session runs — that answer is dropped (no list) rather than shown.
function toState(k: string, daemonId: string | undefined, r: FetchedModelList): { key: string } & EngineModelsState {
  const usable = r.source === 'daemon' && !daemonId ? [] : r.models
  return { key: k, models: usable, loading: false, fallback: false, source: r.source, engineEfforts: r.engineEfforts, refreshing: r.refreshing, error: r.error }
}

// useEngineModels starts on the engine's built-in list (so a picker can render
// and preselect at once) — or on the cached answer, with opts.cache — and
// swaps in the server's list when it answers. Changing engine or daemon
// starts over for the new one.
export function useEngineModels(engine: string, daemonId?: string, opts?: UseEngineModelsOptions): EngineModelsState {
  const key = engineKey(engine)
  const enabled = opts?.enabled ?? true
  const cache = opts?.cache
  const runtime = opts?.runtime
  const [reload, setReload] = useState(0)
  const forceNext = useRef(false)
  const stateKey = JSON.stringify([key, daemonId ?? '', runtime ?? '', cache?.identity?.() ?? ''])
  const [state, setState] = useState<{ key: string } & EngineModelsState>(() => {
    const cached = enabled ? cache?.get(key, daemonId, runtime) : undefined
    return cached
      ? toState(stateKey, daemonId, cached)
      : { key: stateKey, models: builtinModels(key), loading: runtime !== 'cluster', refreshing: runtime === 'cluster', fallback: false, engineEfforts: [] }
  })
  useEffect(() => {
    if (!enabled) return
    let live = true
    let poll: ReturnType<typeof setTimeout> | undefined
    const identity = cache?.identity?.()
    const k = stateKey
    const force = forceNext.current
    forceNext.current = false
    const load = (forceRefresh = false) => {
      void fetchModelList(key, daemonId, runtime, forceRefresh)
        .then(answer => {
          if (!live || cache?.identity?.() !== identity) return
          const kept = cache?.get(key, daemonId, runtime)
          const shown = (answer.refreshing || answer.error) && answer.models.length === 0 && kept
            ? { ...answer, models: kept.models, engineEfforts: answer.engineEfforts.length ? answer.engineEfforts : kept.engineEfforts }
            : answer
          if (!answer.refreshing && !answer.error) cache?.put(key, daemonId, answer, runtime)
          setState(toState(k, daemonId, shown))
          if (runtime === 'cluster' && answer.refreshing) poll = setTimeout(load, 2000)
        })
        .catch((err: unknown) => {
          if (!live || cache?.identity?.() !== identity) return
          const error = err instanceof Error ? err.message : 'Could not load models.'
          const kept = cache?.get(key, daemonId, runtime)
          setState(kept ? { ...toState(k, daemonId, kept), error } : { key: k, models: builtinModels(key), loading: false, fallback: true, engineEfforts: [], error })
        })
    }
    load(force)
    return () => { live = false; clearTimeout(poll) }
  }, [key, daemonId, runtime, stateKey, enabled, cache, reload])
  const retry = runtime === 'cluster' ? () => {
    forceNext.current = true
    setState(previous => ({ ...previous, error: undefined, refreshing: true }))
    setReload(value => value + 1)
  } : undefined
  if (enabled && state.key === stateKey) return { ...state, retry }
  // Until this engine's (and daemon's) answer lands, show its cached list, or
  // the engine's built-in list — never the last one's.
  const cached = enabled ? cache?.get(key, daemonId, runtime) : undefined
  return cached
    ? { ...toState(stateKey, daemonId, cached), retry }
    : { models: builtinModels(key), loading: runtime !== 'cluster', refreshing: enabled && runtime === 'cluster', fallback: false, engineEfforts: [], retry }
}

// ─── Selection rules ─────────────────────────────────────────────────────────

export function defaultModel(engine: string, models: readonly EngineModel[]): EngineModel | undefined {
  const prefix = DEFAULT_MODEL_PREFIX[engineKey(engine)]
  const main = models.filter(m => m.section === 'main')
  const preferred = prefix ? (main.find(m => m.id.startsWith(prefix)) ?? models.find(m => m.id.startsWith(prefix))) : undefined
  return preferred ?? main[0] ?? models[0]
}

// modelDefaultEffort: what to preselect for a model — its own default;
// undefined when it takes none, and also when it has levels but no default
// (Hermes, which doesn't say): that is "Auto", which sends no effort and
// leaves it to the engine's own configuration.
export function modelDefaultEffort(model: EngineModel | undefined): string | undefined {
  if (!model || model.efforts.length === 0) return undefined
  if (model.default_effort && model.efforts.includes(model.default_effort)) return model.default_effort
  return undefined
}

// hasAutoEffort: the model has effort levels but no default of its own, so
// the picker offers "Auto" (no effort sent) alongside them.
export function hasAutoEffort(model: EngineModel | undefined): boolean {
  return !!model && model.efforts.length > 0 && modelDefaultEffort(model) === undefined
}

// effectiveEffort: an explicit choice wins while the model supports it;
// otherwise the model's default (re-preselected whenever the model changes),
// which is undefined — Auto — for a model without one.
export function effectiveEffort(model: EngineModel | undefined, explicit: string | null): string | undefined {
  if (!model || model.efforts.length === 0) return undefined
  if (explicit && model.efforts.includes(explicit)) return explicit
  return modelDefaultEffort(model)
}
