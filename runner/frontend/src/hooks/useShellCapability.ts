import { useEffect, useState } from 'react'
import { apiFetch } from '../apiFetch'
import { signedInAccount } from '../lib/liveBoardCard'

// Capability is a revocable display hint. The server separately authorizes each
// open/input/output; cached UI state never grants execution authority.
export function useShellCapability(sessionId: string | undefined, status: string | undefined) {
  const [available, setAvailable] = useState<string | null>(null)
  useEffect(() => {
    if (!sessionId) return
    let request: AbortController | undefined
    const account = signedInAccount()
    const refresh = () => {
      request?.abort()
      const abort = new AbortController(); request = abort
      setAvailable(null)
      void apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/shell-capability`, { signal: abort.signal })
        .then(async response => response.ok ? await response.json() as { available?: boolean } : null)
        .then(reply => { if (!abort.signal.aborted && signedInAccount() === account) setAvailable(reply?.available === true ? sessionId : null) })
        .catch(() => { /* Unknown or old peers fail closed. */ })
    }
    refresh()
    window.addEventListener('blerg-desktop-scope-changed', refresh)
    window.addEventListener('focus', refresh)
    return () => { request?.abort(); window.removeEventListener('blerg-desktop-scope-changed', refresh); window.removeEventListener('focus', refresh) }
  }, [sessionId, status])
  return available === sessionId
}
