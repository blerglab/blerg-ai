// Dismiss in Needs you (card #155): the person closes a review request or a question without
// deciding or answering it (they may have answered in the chat instead). The item leaves at once and
// a toast offers Undo for DISMISS_UNDO_MS; only then is the dismissal sent, so Undo never has to take
// anything back from the agent. A failure puts the item back and says why.
import { create } from 'zustand'
import { apiFetch } from '../apiFetch'
import { useToastStore } from '../hooks/useToastStore'
import { notifyReviewRequestsChanged } from '../hooks/useMyReviewRequests'

export const DISMISS_UNDO_MS = 5000

/** What is dismissed: "review:<request id>" or "ask:<message id>". */
export type DismissKey = `review:${string}` | `ask:${string}`

interface DismissStore {
  /** Hidden from Needs you and the bell: dismissed, or waiting out its Undo. */
  hidden: ReadonlySet<DismissKey>
  hide: (key: DismissKey) => void
  show: (key: DismissKey) => void
}

export const useDismissStore = create<DismissStore>(set => ({
  hidden: new Set(),
  hide: key => set(s => ({ hidden: new Set(s.hidden).add(key) })),
  show: key => set(s => {
    const hidden = new Set(s.hidden)
    hidden.delete(key)
    return { hidden }
  }),
}))

/** What the rail knows of an open question (AskLive). */
export interface DismissableAsk { messageId: string; noWait?: boolean; ownerOnly?: boolean }

/** Whether the viewer may dismiss ask, as the server decides it (POST /api/messages/{id}/dismiss):
 *  never an engine command approval (owner-only and waited on: allowed or denied, not dismissed),
 *  and an owner-only or no-wait question only by the session's owner. isOwner undefined: not known
 *  yet (the chat's own reading), so the server decides. */
export function canDismissAsk(ask: DismissableAsk, isOwner: boolean | undefined, archived: boolean): boolean {
  if (!ask.messageId || archived) return false
  if (ask.ownerOnly && !ask.noWait) return false
  if ((ask.ownerOnly || ask.noWait) && isOwner === false) return false
  return true
}

/** The request that dismisses key on the server. keepalive: the page is going away, so the request
 *  must outlive it. */
function send(key: DismissKey, keepalive = false): Promise<Response> {
  const [kind, id] = [key.slice(0, key.indexOf(':')), key.slice(key.indexOf(':') + 1)]
  if (kind === 'review') {
    return apiFetch(`/api/review-requests/${encodeURIComponent(id)}/decide`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ decision: 'dismiss' }), keepalive,
    })
  }
  return apiFetch(`/api/messages/${encodeURIComponent(id)}/dismiss`, { method: 'POST', keepalive })
}

async function reason(resp: Response): Promise<string> {
  try {
    const body = await resp.json() as { error?: unknown }
    if (typeof body.error === 'string' && body.error) return body.error
  } catch { /* not JSON */ }
  return `the server said ${resp.status}`
}

interface Pending { timer: ReturnType<typeof setTimeout>; what: string; toastId: number }
const timers = new Map<DismissKey, Pending>()

/** Sends key's dismissal now. A 409 means it was already closed (the agent resolved it, or it was
 *  decided in another tab): nothing to say, and the list drops it by itself. */
function deliver(key: DismissKey, what: string, keepalive = false) {
  const back = (body: string) => {
    useDismissStore.getState().show(key)
    useToastStore.getState().addToast({ title: 'Could not dismiss', body: `${what}: ${body}` })
  }
  void send(key, keepalive).then(async resp => {
    if (key.startsWith('review:')) notifyReviewRequestsChanged()
    if (resp.ok) return
    if (resp.status === 409) {
      useDismissStore.getState().show(key)
      return
    }
    back(await reason(resp))
  }, () => back('the server could not be reached'))
}

/** Sends every dismissal still waiting out its Undo: the tab is closing or going to the background
 *  (where a phone freezes timers), and the person did not undo it. */
export function flushPendingDismissals() {
  for (const [key, p] of [...timers]) {
    clearTimeout(p.timer)
    timers.delete(key)
    useToastStore.getState().removeToast(p.toastId)
    deliver(key, p.what, true)
  }
}

if (typeof window !== 'undefined') {
  window.addEventListener('pagehide', flushPendingDismissals)
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'hidden') flushPendingDismissals()
  })
}

/** Hides key, shows "Dismissed <what>" with Undo, and sends the dismissal once the Undo time is up. */
export function dismissWithUndo(key: DismissKey, what: string) {
  if (timers.has(key)) return
  const store = useDismissStore.getState()
  const toasts = useToastStore.getState()
  store.hide(key)
  const undo = () => {
    const pending = timers.get(key)
    if (pending === undefined) return
    clearTimeout(pending.timer)
    timers.delete(key)
    useDismissStore.getState().show(key)
    useToastStore.getState().removeToast(toastId)
  }
  const toastId = toasts.addToast({ title: 'Dismissed', body: what, action: { label: 'Undo', onClick: undo }, ttlMs: DISMISS_UNDO_MS })
  const timer = setTimeout(() => {
    timers.delete(key)
    deliver(key, what)
  }, DISMISS_UNDO_MS)
  timers.set(key, { timer, what, toastId })
}

/** Whether key is hidden (dismissed, or waiting out its Undo). */
export function useDismissed(): (key: DismissKey) => boolean {
  const hidden = useDismissStore(s => s.hidden)
  return key => hidden.has(key)
}
