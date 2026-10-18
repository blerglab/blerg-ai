import { useCallback, useEffect, useRef, useState } from 'react'
import { apiFetch } from '../apiFetch'
import type { SessionPluginSettings } from '../lib/sessionPlugins'

export function useSessionPlugins(sessionId: string, active: boolean) {
  const [loaded, setLoaded] = useState<{ id: string; data: SessionPluginSettings } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const generation = useRef(0)
  const refresh = useCallback(async () => {
    const seq = ++generation.current
    try {
      const res = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/plugins`)
      if (!res.ok) throw new Error('Could not load plugin choices. Try again.')
      const body = (await res.json()) as SessionPluginSettings
      const allowed = body.allowed_marketplaces ?? []
      const plugins = body.plugins ?? []
      const requests = body.requests ?? []
      if (
        typeof body.supported !== 'boolean' ||
        !Array.isArray(allowed) ||
        !allowed.every((s) => typeof s === 'string') ||
        !Array.isArray(plugins) ||
        !Array.isArray(requests)
      )
        throw new Error('Could not load plugin choices. Try again.')
      if (seq !== generation.current) return
      setLoaded({
        id: sessionId,
        data: {
          ...body,
          allowed_marketplaces: allowed,
          plugins,
          requests,
          any_marketplace: body.any_marketplace === true,
        },
      })
      setError(null)
    } catch {
      if (seq === generation.current) setError('Could not load plugin choices. Try again.')
    }
  }, [sessionId])
  const invalidate = useCallback(() => {
    generation.current++
  }, [])
  useEffect(() => {
    if (!active) return
    let canceled = false
    void Promise.resolve().then(() => {
      if (!canceled) return refresh()
    })
    const tick = () => {
      if (!document.hidden) void refresh()
    }
    const timer = setInterval(tick, 5000)
    return () => {
      canceled = true
      clearInterval(timer)
      invalidate()
    }
  }, [active, refresh, invalidate])
  return { sessionId, data: loaded?.id === sessionId ? loaded.data : null, error, refresh }
}
