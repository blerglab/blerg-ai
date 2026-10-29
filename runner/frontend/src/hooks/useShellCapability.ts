import { useEffect, useState } from 'react'
import { apiFetch } from '../apiFetch'
import { signedInAccount } from '../lib/liveBoardCard'

// Capability is a revocable display hint. The server separately authorizes each
// open/input/output; cached UI state never grants execution authority.
export const SHELL_BUSY_RETRIES = 5
export const SHELL_BUSY_RETRY_MS = 1000
export function useShellCapability(sessionId: string | undefined, status: string | undefined) {
  const [capability, setCapability] = useState<{ session: string; available: boolean } | null>(null)
  useEffect(() => {
    if (!sessionId) return
    let request: AbortController | undefined
    let retry: ReturnType<typeof setTimeout> | undefined
    const account = signedInAccount()
    const refresh = (attempt = 0) => {
      request?.abort(); clearTimeout(retry)
      const abort = new AbortController(); request = abort
      if (attempt === 0) setCapability(null)
      void apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/shell-capability`, { signal: abort.signal })
        .then(async response => {
          if (response.ok) return await response.json() as { available?: boolean }
          // A busy desktop is not a "no": stay pending and ask again shortly.
          const body = await response.json().catch(() => null) as { code?: unknown } | null
          return response.status === 503 && body?.code === 'runtime_busy' && attempt < SHELL_BUSY_RETRIES ? 'busy' as const : null
        })
        .then(reply => {
          if (abort.signal.aborted || signedInAccount() !== account) return
          if (reply === 'busy') { retry = setTimeout(() => refresh(attempt + 1), SHELL_BUSY_RETRY_MS); return }
          setCapability({ session: sessionId, available: reply?.available === true })
        })
        .catch(() => { if (!abort.signal.aborted && signedInAccount() === account) setCapability({ session: sessionId, available: false }) })
    }
    const restart = () => refresh()
    const workspaceReady = (event: Event) => { if ((event as CustomEvent<string>).detail === sessionId) refresh() }
    refresh()
    window.addEventListener('blerg-session-workspace-ready', workspaceReady)
    window.addEventListener('blerg-desktop-scope-changed', restart)
    window.addEventListener('blerg-desktop-inventory-changed', restart)
    window.addEventListener('focus', restart)
    return () => { request?.abort(); clearTimeout(retry); window.removeEventListener('blerg-session-workspace-ready', workspaceReady); window.removeEventListener('blerg-desktop-scope-changed', restart); window.removeEventListener('blerg-desktop-inventory-changed', restart); window.removeEventListener('focus', restart) }
  }, [sessionId, status])
  // A confirmed refusal must release focus restoration; loading must wait for handles.
  return capability && capability.session === sessionId ? capability.available : null
}
