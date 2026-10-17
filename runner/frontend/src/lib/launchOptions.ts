import { useEffect, useState } from 'react'
import { apiFetch } from '../apiFetch'
import { parseModelList, type EngineModelsState } from './engineModels'
import type { ModelList } from '../types'

export interface LaunchEngine {
  id: string
  name: string
  runtimes: string[]
  terminal: boolean
  restricted: boolean
  plugins: boolean
  credential_import: boolean
  daemon_id?: string
  runtime?: string
  models: ModelList & { refreshing?: boolean; error?: string }
}
interface Snapshot {
  key: string
  state: 'loading' | 'known' | 'unknown'
  engines: LaunchEngine[]
  error?: string
  refreshing?: boolean
}
interface Target { runtime: string; daemonId?: string; kind?: string; restricted?: boolean; personalOnly?: boolean; enabled?: boolean }
const ID = /^[a-z][a-z0-9_-]{0,63}$/

/** Available engines for this principal and target, not the install's global brand inventory.
 * Target changes synchronously hide the prior result; bounded requests and generation cleanup
 * prevent a dead service or late response from leaving stale launch choices enabled. */
export function useLaunchOptions({ runtime, daemonId, kind = 'agent', restricted = false, personalOnly = false, enabled = true }: Target) {
  const query = new URLSearchParams({ runtime, kind })
  if (daemonId && runtime !== 'cluster') query.set('daemon_id', daemonId)
  if (restricted) query.set('restricted', '1')
  if (personalOnly) query.set('personal_only', '1')
  const key = query.toString()
  const [revision, setRevision] = useState(0)
  const requestKey = `${key}\0${revision}`
  const [snapshot, setSnapshot] = useState<Snapshot>({ key: '', state: 'loading', engines: [] })
  useEffect(() => {
    if (!enabled) return
    let live = true
    const controller = new AbortController()
    let poll: ReturnType<typeof setTimeout> | undefined
    let forceRefresh = revision > 0
    const timer = setTimeout(() => {
      if (!live) return
      live = false
      controller.abort()
      clearTimeout(poll)
      setSnapshot(previous => previous.key === requestKey && previous.state === 'known'
        ? { ...previous, refreshing: false, error: 'Model discovery timed out. Retry to refresh choices.' }
        : { key: requestKey, state: 'unknown', engines: [], error: 'Engine discovery timed out. Retry or check the selected runtime.' })
    }, 8_000)
    const read = () => {
      const refreshQuery = forceRefresh ? '&refresh=1' : ''
      forceRefresh = false
      void apiFetch(`/api/runner/launch-options?${key}${refreshQuery}`, { signal: controller.signal })
        .then(async response => {
          if (!response.ok) throw new Error('Engine discovery unavailable.')
          const value: unknown = await response.json()
          if (!value || typeof value !== 'object') throw new Error('Engine discovery returned an invalid response.')
          const raw = value as Record<string, unknown>
          if ((raw.state !== 'known' && raw.state !== 'unknown') || !Array.isArray(raw.engines)) throw new Error('Engine discovery returned an invalid response.')
          const engines: LaunchEngine[] = []
          for (const item of raw.engines) {
            if (!item || typeof item !== 'object') throw new Error('Engine discovery returned an invalid engine.')
            const engine = item as LaunchEngine
            if (typeof engine.id !== 'string' || !ID.test(engine.id) || typeof engine.name !== 'string' || !engine.name || engines.some(e => e.id === engine.id)) throw new Error('Engine discovery returned an invalid engine.')
            engines.push(engine)
          }
          if (!live) return
          const refreshing = raw.refreshing === true || engines.some(e => e.models?.refreshing === true)
          setSnapshot({ key: requestKey, state: raw.state === 'unknown' && refreshing ? 'loading' : raw.state, engines: raw.state === 'known' ? engines : [], refreshing, error: typeof raw.error === 'string' ? raw.error : undefined })
          if (refreshing) poll = setTimeout(read, 500)
          else clearTimeout(timer)
        })
        .catch(() => {
          if (!live) return
          clearTimeout(timer)
          setSnapshot({ key: requestKey, state: 'unknown', engines: [], error: 'Engine discovery unavailable. Retry or check the selected runtime.' })
        })
    }
    read()
    return () => { live = false; clearTimeout(timer); clearTimeout(poll); controller.abort() }
  }, [key, requestKey, enabled, revision])
  const current: Snapshot = !enabled ? { key, state: 'unknown', engines: [], error: 'Choose a connected runtime to discover available engines.' } : snapshot.key === requestKey ? snapshot : { key, state: 'loading', engines: [] }
  return { ...current, retry: () => setRevision(value => value + 1) }
}

/** Models already scoped by the server to this engine's actual eligible host or Cluster image.
 * In particular, an unpinned cron must never substitute the union of workstation catalogs. */
export function launchEngineModels(engine: LaunchEngine | undefined, retry: () => void, discoveryError?: string): EngineModelsState {
  const list = engine?.models
  if (!engine || !list) return { models: [], engineEfforts: [], loading: false, fallback: false, error: discoveryError, retry }
  try {
    return { models: parseModelList(engine.id, list), engineEfforts: list.engine_efforts ?? [], loading: false,
      fallback: false, refreshing: !discoveryError && list.refreshing, error: discoveryError ?? list.error, source: list.source, retry }
  } catch {
    return { models: [], engineEfforts: [], loading: false, fallback: false, error: 'Model choices unavailable.', retry }
  }
}
