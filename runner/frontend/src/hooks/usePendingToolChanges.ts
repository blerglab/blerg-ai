import { useEffect, useState } from 'react'
import { apiFetch } from '../apiFetch'
import { countPendingToolChanges } from '../lib/toolChanges'

/** usePendingToolChanges: how many MCP tool changes on this session wait on a decision (unmuted,
 *  never removed; like the Proposals badge), or null until the first read answers. Read when the session opens and again whenever
 *  `refresh` changes (the side panel opening, or its Capabilities tab coming on screen). An agent session only:
 *  `enabled` false reads nothing. A failed read keeps the last value. */
export function usePendingToolChanges(sessionId: string, enabled: boolean, refresh: unknown): number | null {
  const [count, setCount] = useState<{ sessionId: string; n: number } | null>(null)
  useEffect(() => {
    if (!enabled) return
    let cancelled = false
    apiFetch(`/api/tool-changes?scope=open&session=${encodeURIComponent(sessionId)}`)
      .then(async res => (res.ok ? (await res.json() as { tool_changes?: { muted?: boolean; kind?: string }[] }) : null))
      .then(body => { if (!cancelled && body) setCount({ sessionId, n: countPendingToolChanges(body.tool_changes) }) })
      .catch(() => { /* leave the last value */ })
    return () => { cancelled = true }
  }, [sessionId, enabled, refresh])
  return enabled && count?.sessionId === sessionId ? count.n : null
}
