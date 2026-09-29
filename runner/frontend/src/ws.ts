import type { BrowserMessage, ServerMessage } from './types'
import {
  consumeAccessTokenFromFragment,
  getAccessToken,
  redirectToRefresh,
} from './authClient'
import { refreshAlreadyAttempted, markRefreshAttempted, clearRefreshAttempted } from './refreshGuard'
import { tokenExpired } from './claims'

let socket: WebSocket | null = null
let backoff = 2000
let reconnectTimer: ReturnType<typeof setTimeout> | null = null
const handlers = new Map<string, Set<(msg: ServerMessage) => void>>()
const openHandlers = new Set<() => void>()

function clearReconnectTimer(): void {
  if (reconnectTimer !== null) {
    clearTimeout(reconnectTimer)
    reconnectTimer = null
  }
}

function scheduleReconnect(): void {
  clearReconnectTimer()
  reconnectTimer = setTimeout(connect, backoff)
  backoff = Math.min(backoff * 2, 30000)
}

function connect(): void {
  clearReconnectTimer()
  // Never open a second socket on top of a live or in-flight one.
  if (
    socket &&
    (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)
  ) {
    return
  }

  // /ws/browser is gated exactly like runner's REST API. The browser
  // WebSocket API can't set an Authorization header, so the access token
  // travels as the second entry of the subprotocol list: the server reads it
  // out of Sec-WebSocket-Protocol and echoes back only the "bearer" marker
  // (see runner/internal/server/browser_conn.go).
  const token = getAccessToken()
  if (!token) {
    // No token in memory (fresh load, or the session expired): the socket
    // would just be refused. Go get one, the same way apiFetch does on a 401 —
    // once per page load, so a failure to obtain one can't loop.
    if (!refreshAlreadyAttempted()) {
      markRefreshAttempted()
      redirectToRefresh(location.href)
    }
    return
  }

  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const url = `${protocol}//${location.host}/ws/browser`
  const ws = new WebSocket(url, ['bearer', token])
  socket = ws

  let opened = false

  ws.onopen = () => {
    opened = true
    clearRefreshAttempted()
    backoff = 2000
    // Let consumers re-establish per-session state (subscriptions, resize)
    // on every fresh connection, not just the first.
    openHandlers.forEach(h => {
      try {
        h()
      } catch {
        // ignore handler errors
      }
    })
  }

  ws.onmessage = (event: MessageEvent) => {
    let msg: ServerMessage
    try {
      msg = JSON.parse(event.data as string) as ServerMessage
    } catch {
      return
    }
    const set = handlers.get(msg.type)
    if (set) {
      set.forEach(handler => handler(msg))
    }
  }

  ws.onclose = () => {
    if (socket === ws) socket = null
    // A socket that closed without ever opening was almost certainly refused
    // at the upgrade — but that's ambiguous: it could mean the access token
    // we sent has since expired, OR that the server is just unreachable
    // (down, network blip). Redirecting to /auth/refresh on the latter would
    // bounce the user through a refresh loop for an outage that has nothing
    // to do with their session. So only treat it as an expired-token signal
    // when the token we hold actually looks expired (claims.ts's cheap,
    // UNVERIFIED decode — UX heuristic only, never authoritative); otherwise
    // fall through to the normal backoff-and-retry path below.
    //
    // Guarded to once per page load (and reset by a successful open above) so
    // even a genuinely expired-token loop can't turn into a redirect loop.
    if (!opened) {
      const tok = getAccessToken()
      if ((!tok || tokenExpired(tok)) && !refreshAlreadyAttempted()) {
        markRefreshAttempted()
        redirectToRefresh(location.href)
        return
      }
    }
    scheduleReconnect()
  }

  ws.onerror = () => {
    // onclose fires after onerror; reconnect is handled there.
  }
}

/** send writes msg to the socket; false when it is not open (the message is
 *  dropped — callers that promise the user something can say so). */
export function send(msg: BrowserMessage): boolean {
  if (socket && socket.readyState === WebSocket.OPEN) {
    socket.send(JSON.stringify(msg))
    return true
  }
  return false
}

export function onMessage<T extends ServerMessage>(
  type: T['type'],
  handler: (msg: T) => void
): () => void {
  if (!handlers.has(type)) {
    handlers.set(type, new Set())
  }
  handlers.get(type)!.add(handler as (msg: ServerMessage) => void)
  return () => {
    handlers.get(type)?.delete(handler as (msg: ServerMessage) => void)
  }
}

// onOpen registers a callback fired on every (re)connection. If the socket is
// already open, it fires immediately so callers can subscribe right away.
// Returns an unsubscribe function.
export function onOpen(handler: () => void): () => void {
  openHandlers.add(handler)
  if (socket && socket.readyState === WebSocket.OPEN) {
    handler()
  }
  return () => {
    openHandlers.delete(handler)
  }
}

// Reconnect immediately when the tab/app returns to the foreground. Background
// tabs get their socket suspended and closed by the browser; waiting out the
// backoff timer would leave input dead for up to 30s after returning.
if (typeof document !== 'undefined') {
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState !== 'visible') return
    if (
      !socket ||
      socket.readyState === WebSocket.CLOSED ||
      socket.readyState === WebSocket.CLOSING
    ) {
      backoff = 2000
      connect()
    }
  })
  // Also catch plain window refocus (desktop alt-tab without a visibility flip).
  window.addEventListener('online', () => {
    backoff = 2000
    connect()
  })
}

// Start connecting immediately on module load.
//
// Consume the #access_token fragment FIRST. main.tsx also calls this (it's
// idempotent — a second call finds no fragment and is a no-op), but ES module
// imports are evaluated before main.tsx's own body runs, so by the time that
// call happens this module has already tried to connect. Without this, the very
// first load after a refresh redirect would find no token and bounce straight
// back to /auth/refresh in a loop.
consumeAccessTokenFromFragment()
connect()
