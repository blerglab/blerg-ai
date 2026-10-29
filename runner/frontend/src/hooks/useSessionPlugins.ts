import { useCallback, useEffect, useRef, useState } from 'react'
import { apiFetch } from '../apiFetch'
import type { SessionPluginSettings } from '../lib/sessionPlugins'
import { sessionView } from '../lib/sessionView'

export function useSessionPlugins(sessionId: string, active: boolean) {
  // The last choices read for this session (the explorer and panel cache) show at once.
  const [loaded, setLoaded] = useState<{ id: string; data: SessionPluginSettings } | null>(() => {
    const data = sessionView<SessionPluginSettings>(sessionId, 'plugins').cached
    return data ? { id: sessionId, data } : null
  })
  const [error, setError] = useState<string | null>(null)
  const generation = useRef(0)
  const reading = useRef<AbortController | null>(null)
  const refresh = useCallback(async () => {
    const seq = ++generation.current
    const cache = sessionView<SessionPluginSettings>(sessionId, 'plugins')
    // One read at a time: a newer one (or leaving) ends the one still out.
    reading.current?.abort()
    const ac = new AbortController()
    reading.current = ac
    try {
      const res = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/plugins`, { signal: ac.signal })
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
      const data = {
        ...body,
        allowed_marketplaces: allowed,
        plugins,
        requests,
        any_marketplace: body.any_marketplace === true,
      }
      setLoaded({ id: sessionId, data })
      cache.keep(data)
      setError(null)
    } catch {
      if (seq === generation.current) setError('Could not load plugin choices. Try again.')
    }
  }, [sessionId])
  const invalidate = useCallback(() => {
    generation.current++
    reading.current?.abort()
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
