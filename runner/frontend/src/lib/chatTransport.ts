// One transport for the page: the runner's shared socket and its browser token. A token that
// looks expired is renewed before a request; a route that still answers 401 sends the page to
// core's refresh, as apiFetch does. The chat and the session panel (a diff's counts) share it.
import { createBlergTransport, useTranscriptStore, type Transport } from '@blerglab/chat'
import * as ws from '../ws'
import { ensureFreshToken, getAccessToken } from '../authClient'
import { bounceToRefresh } from '../signIn'
import { apiFetch } from '../apiFetch'
import { tokenExpired, tokenSubject } from '../claims'
import { checkAccount } from './liveBoardCard'
import { notifyReviewRequestsChanged } from '../hooks/useMyReviewRequests'
import { rememberDesktopCacheScope } from './desktopCacheScope'

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
  const datasetInvalidations = new Set<() => void>()
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
  // Server-marked bound decisions need the current human token.
  // Ordinary answers retain the durable socket input and fallback path.
  const send = shared.send.bind(shared)
  shared.send = async (sessionId, text, options) => {
    if (!alive) return {queued: false}
    if (!options?.answerTo) return send(sessionId, text, options)
    const boundDecision = useTranscriptStore.getState().sessions[sessionId]?.events.some(event => {
      const payload = event.payload as {message_id?: unknown; live_binding?: unknown; bound?: unknown} | null
      return event.kind === 'ask' && payload?.message_id === options.answerTo && (payload?.bound === true || payload?.live_binding === true)
    })
    if (!boundDecision) return send(sessionId, text, options)
    const response = await apiFetch(`/api/messages/${encodeURIComponent(options.answerTo)}/reply`, {
      method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({answer: text, ...(options.clientId ? {client_id: options.clientId} : {})}),
    })
    return {queued: response.ok}
  }
  const subscribe = shared.subscribe.bind(shared)
  shared.subscribe = (...args) => {
    rememberDesktopCacheScope(args[0])
    const off = subscribe(...args)
    const remove = () => { if (subscriptions.delete(remove)) off() }
    subscriptions.add(remove)
    return remove
  }
  if (shared.files) shared.files.onDatasetInvalidated = listener => {datasetInvalidations.add(listener); return () => {datasetInvalidations.delete(listener)}}
  const datasets = shared.files?.subscribeDataset?.bind(shared.files)
  if (datasets && shared.files) shared.files.subscribeDataset = (sessionId, data, receive) => {
    rememberDesktopCacheScope(sessionId)
    const off = datasets(sessionId, data, receive)
    const purge = () => {
      if (!subscriptions.delete(purge)) return
      off()
      receive({type: 'dataset_delta', subscription_id: '', epoch: '', base_sequence: 0, sequence: 0, snapshot: true, upserts: [], removals: [], rows: [], observed_at: '', status: 'access_removed'})
    }
    subscriptions.add(purge)
    return () => {if (subscriptions.delete(purge)) off()}
  }
  const loadOlder = shared.loadOlder?.bind(shared)
  if (loadOlder) shared.loadOlder = (...args) => {
    rememberDesktopCacheScope(args[0])
    return loadOlder(...args)
  }
  disposeShared = () => {
    // Unsubscribe first, so closing pending older reads cannot flush an old
    // subscription's buffered capability events back into the transcript.
    for (const off of subscriptions) off()
    for (const purge of datasetInvalidations) purge()
    datasetInvalidations.clear()
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
