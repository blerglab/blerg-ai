// Read the complete owner-only queue, independently of loaded transcript events or permissions.
import { useCallback, useEffect, useRef, useState } from 'react'
import { apiFetch } from '../apiFetch'
import { PROPOSALS_POLL_MS } from '../lib/proposals'
import { serverClockOffset } from '../lib/proposalAttention'
import type { ProposalInfo } from '../types'

export function useProposalInbox() {
  const [pending, setPending] = useState<ProposalInfo[]>([])
  const [error, setError] = useState<string | null>(null)
  // This client's clock minus the server's, from the list's server_time (lib/proposalAttention).
  const [clockOffset, setClockOffset] = useState(0)
  const generation = useRef(0)
  const refresh = useCallback(async () => {
    const seq = ++generation.current
    try {
      const res = await apiFetch('/api/proposals?scope=open&state=pending')
      if (!res.ok) {
        if (res.status === 401 || res.status === 403) {
          if (seq === generation.current) { setPending([]); setError(null) }
          return
        }
        throw new Error('Could not refresh proposals. Retry.')
      }
      const body = await res.json() as { proposals?: ProposalInfo[]; server_time?: string }
      if (seq !== generation.current) return
      // Older servers may not offer the queue.
      if (!Array.isArray(body.proposals)) return
      setPending(body.proposals.filter(p => p.state === 'pending'))
      setClockOffset(serverClockOffset(body.server_time))
      setError(null)
    } catch { if (seq === generation.current) setError('Could not refresh proposals. Retry.') }
  }, [])
  const invalidate = useCallback(() => { generation.current++ }, [])
  useEffect(() => {
    const tick = () => { if (!document.hidden) void refresh() }
    tick()
    const timer = setInterval(tick, PROPOSALS_POLL_MS)
    window.addEventListener('focus', tick)
    window.addEventListener('blerg:proposals-changed', tick)
    document.addEventListener('visibilitychange', tick)
    return () => {
      invalidate()
      clearInterval(timer)
      window.removeEventListener('focus', tick)
      window.removeEventListener('blerg:proposals-changed', tick)
      document.removeEventListener('visibilitychange', tick)
    }
  }, [refresh, invalidate])
  return { pending, refresh, error, clockOffset }
}

// clockOffset is optional for a caller that builds an inbox by hand (0: the client's clock).
export type ProposalInbox = Omit<ReturnType<typeof useProposalInbox>, 'clockOffset'> & { clockOffset?: number }
export function proposalsChanged() { window.dispatchEvent(new Event('blerg:proposals-changed')) }
