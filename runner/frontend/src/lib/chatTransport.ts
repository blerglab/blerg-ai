// One transport for the page: the runner's shared socket and its browser token. A token that
// looks expired is renewed before a request; a route that still answers 401 sends the page to
// core's refresh, as apiFetch does. The chat and the session panel (a diff's counts) share it.
import { createBlergTransport, type Transport } from '@blerglab/chat'
import * as ws from '../ws'
import { ensureFreshToken, getAccessToken } from '../authClient'
import { bounceToRefresh } from '../signIn'
import { tokenExpired, tokenSubject } from '../claims'
import { checkAccount } from './liveBoardCard'
import { notifyReviewRequestsChanged } from '../hooks/useMyReviewRequests'

let shared: Transport | null = null
let disposeShared: (() => void) | null = null

/** Drop buffered replay pages and pending older-page reads before a new scope subscribes. */
export function invalidateChatTransport() {
  const dispose = disposeShared
  shared = null
  disposeShared = null
  dispose?.()
}
export function blergTransport(): Transport {
  if (shared) return shared
  const subscriptions = new Set<() => void>()
  const listeners = new Set<() => void>()
  const closes = new Set<() => void>()
  let alive = true
  const track = (off: () => void) => {
    const remove = () => { if (listeners.delete(remove)) off() }
    listeners.add(remove)
    return remove
  }
  shared = createBlergTransport({
    socket: {
      send: message => alive && ws.send(message),
      connected: ws.connected,
      onMessage: (type, handler) => track(ws.onMessage(type as Parameters<typeof ws.onMessage>[0], message => { if (alive) handler(message as never) })),
      onOpen: handler => track(ws.onOpen(() => { if (alive) handler() })),
      onClose: handler => {
        closes.add(handler)
        const off = ws.onClose(handler)
        return track(() => { closes.delete(handler); off() })
      },
    },
    getToken: async () => {
      const tok = getAccessToken()
      if (!tok || tokenExpired(tok)) await ensureFreshToken()
      // A renewal may have signed in someone else: what was read under the other account goes.
      checkAccount()
      return getAccessToken()
    },
    onUnauthorized: () => {
      bounceToRefresh()
    },
    // Who is signed in: a question someone else dismissed says so (not "dismissed by you").
    viewerId: () => tokenSubject(getAccessToken()),
  })
  const subscribe = shared.subscribe.bind(shared)
  shared.subscribe = (...args) => {
    const off = subscribe(...args)
    const remove = () => { if (subscriptions.delete(remove)) off() }
    subscriptions.add(remove)
    return remove
  }
  disposeShared = () => {
    // Unsubscribe first, so closing pending older reads cannot flush an old
    // subscription's buffered capability events back into the transcript.
    for (const off of subscriptions) off()
    alive = false
    for (const close of closes) close()
    for (const off of listeners) off()
  }
  // A review decided in the viewer leaves the bell at once, not at its next minute's read.
  const decide = shared.decideReview?.bind(shared)
  if (decide) {
    shared.decideReview = async (...args) => {
      try {
        return await decide(...args)
      } finally {
        notifyReviewRequestsChanged()
      }
    }
  }
  return shared
}
