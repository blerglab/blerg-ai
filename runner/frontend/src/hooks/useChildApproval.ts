// useChildApproval: a parent session's child approvals (GET/PUT /api/sessions/{id}/child-approval),
// read and written as authoritative server snapshots, never optimistic or inferred from an engine
// name. A stalled read or write is bounded, a late answer is ignored, and a 409 re-reads the current
// scope without retrying. The panel's child control and the permissions modal both draw from it.
import { useCallback, useEffect, useRef, useState } from 'react'
import { apiFetch } from '../apiFetch'

export type ChildRuntime = 'docker' | 'daemon' | 'cluster'
export interface ApprovalRuntime {
  runtime: ChildRuntime
  enabled: boolean
  partial: boolean
  allowed_engines: string[]
  granted_engines: string[]
  standing_rule_engines: string[]
  effective_allowed_engines: string[]
  can_enable: boolean
  reason: string
}
export interface Approval { revision: string; daemon_id: string; allowed_engines: string[]; runtimes: ApprovalRuntime[] }
/** What a write came to: the new snapshot, a scope that changed meanwhile (re-read), or a failure. */
export type ApprovalSave = { ok: true; value: Approval } | { ok: false; conflict: boolean }

const strings = (value: unknown): value is string[] => Array.isArray(value) && value.every(v => typeof v === 'string' && v.length > 0)
export function readApproval(value: unknown): Approval {
  if (!value || typeof value !== 'object') throw new Error('invalid approval')
  const v = value as Approval
  if (typeof v.revision !== 'string' || !v.revision || typeof v.daemon_id !== 'string' || !strings(v.allowed_engines) || !Array.isArray(v.runtimes) || !v.runtimes.length) throw new Error('invalid approval')
  const seen = new Set<string>()
  for (const r of v.runtimes) {
    if (!r || (r.runtime !== 'docker' && r.runtime !== 'daemon' && r.runtime !== 'cluster') || seen.has(r.runtime) || typeof r.enabled !== 'boolean' || typeof r.partial !== 'boolean' || typeof r.can_enable !== 'boolean' || typeof r.reason !== 'string' || !strings(r.allowed_engines) || !strings(r.granted_engines) || !strings(r.standing_rule_engines) || !strings(r.effective_allowed_engines)) throw new Error('invalid approval')
    seen.add(r.runtime)
  }
  return v
}

/** Callers key the component that uses this by session id, so a new parent never shows the previous
 *  parent's authority. */
export function useChildApproval(sessionId: string, active: boolean, refreshKey: string) {
  const [value, setValue] = useState<Approval | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const generation = useRef(0)
  const saveGeneration = useRef(0)
  const controller = useRef<AbortController | null>(null)
  const timeout = useRef<number | undefined>(undefined)
  const busy = useRef(false)
  const mounted = useRef(true)
  const endpoint = `/api/sessions/${encodeURIComponent(sessionId)}/child-approval`
  const invalidate = useCallback(() => { ++generation.current; controller.current?.abort(); window.clearTimeout(timeout.current) }, [])
  const load = useCallback(async (conflict = false) => {
    if (!mounted.current) return
    const version = ++generation.current
    controller.current?.abort(); window.clearTimeout(timeout.current)
    const abort = new AbortController(); controller.current = abort
    const timer = window.setTimeout(() => { abort.abort(); if (version === generation.current) { ++generation.current; ++saveGeneration.current; busy.current = false; setSaving(false); setValue(null); setLoading(false); setError('Could not load child approvals. Try again.') } }, 10_000)
    timeout.current = timer
    try {
      const response = await apiFetch(endpoint, { signal: abort.signal })
      if (version !== generation.current) return
      if (response.status === 404) { setValue(null); setError(''); return }
      if (!response.ok) throw new Error('load')
      const next = readApproval(await response.json())
      if (version === generation.current) { setValue(next); setError(conflict ? 'Child approval scope changed. Review the current engines and try again.' : '') }
    } catch {
      if (version === generation.current && !abort.signal.aborted) { setValue(null); setError('Could not load child approvals. Try again.') }
    } finally { window.clearTimeout(timer); if (version === generation.current) setLoading(false) }
  }, [endpoint])
  useEffect(() => {
    mounted.current = true
    void Promise.resolve().then(() => { if (mounted.current) return load() })
    return () => { mounted.current = false; invalidate() }
  }, [load, invalidate])
  useEffect(() => {
    if (!active) return
    const timer = window.setInterval(() => { if (!busy.current) void load() }, 15_000)
    return () => window.clearInterval(timer)
  }, [active, load])
  useEffect(() => { if (refreshKey) void Promise.resolve().then(() => { if (mounted.current && !busy.current) return load() }) }, [refreshKey, load])

  /** One runtime on or off, against `revision` (the snapshot shown when omitted). */
  const save = useCallback(async (runtime: ChildRuntime, enabled: boolean, revision?: string): Promise<ApprovalSave> => {
    const rev = revision ?? value?.revision
    if (!rev || busy.current) return { ok: false, conflict: false }
    const saveId = ++saveGeneration.current
    busy.current = true; setSaving(true); setError('')
    const version = ++generation.current
    controller.current?.abort(); window.clearTimeout(timeout.current)
    const abort = new AbortController(); controller.current = abort
    let result: ApprovalSave = { ok: false, conflict: false }
    const timer = window.setTimeout(() => { abort.abort(); if (version === generation.current) { ++generation.current; setValue(null); setSaving(false); setError('Could not confirm child approval. Refresh before trying again.'); busy.current = false } }, 10_000)
    timeout.current = timer
    try {
      const response = await apiFetch(endpoint, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ runtime, enabled, revision: rev }), signal: abort.signal })
      if (version !== generation.current) return result
      if (response.status === 409) { result = { ok: false, conflict: true }; await load(true); return result }
      if (!response.ok) throw new Error('save')
      const next = readApproval(await response.json())
      if (version === generation.current) { setValue(next); result = { ok: true, value: next } }
    } catch {
      if (version === generation.current && !abort.signal.aborted) { setValue(null); setError('Could not confirm child approval. Refresh before trying again.') }
    } finally {
      window.clearTimeout(timer)
      if (saveId === saveGeneration.current) {
        busy.current = false
        if (mounted.current) setSaving(false)
      }
    }
    return result
  }, [endpoint, load, value?.revision])

  const refresh = useCallback(() => { setLoading(true); void load() }, [load])
  return { value, loading, saving, error, save, refresh, load }
}
