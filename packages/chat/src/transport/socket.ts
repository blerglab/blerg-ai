// The adapter's own browser socket, for a host that does not inject one: the runner's ws.ts
// without the runner's page-level concerns (no refresh redirects — the host's getToken owns the
// token, and a missing one just waits for the next attempt). /ws/browser is gated like the REST
// API; the browser WebSocket API cannot set a header, so the token travels as the second entry of
// the subprotocol list and the server echoes back only the "bearer" marker.
import type { BlergSocket } from './blerg'

export interface BrowserSocketOptions {
  /** The runner's origin (http(s)://…) or '' for the page's own. */
  baseUrl?: string
  getToken: () => Promise<string | null>
  backoff?: { initial?: number; max?: number }
  /** For tests: the WebSocket constructor to use. */
  WebSocket?: typeof WebSocket
}

/** The socket URL for the runner at baseUrl: ws(s):// on the same host. */
export function browserSocketUrl(baseUrl: string): string {
  if (baseUrl) return baseUrl.replace(/^http/, 'ws').replace(/\/$/, '') + '/ws/browser'
  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${protocol}//${location.host}/ws/browser`
}

export function createBrowserSocket(opts: BrowserSocketOptions): BlergSocket & { close(): void } {
  const WS = opts.WebSocket ?? globalThis.WebSocket
  const initial = opts.backoff?.initial ?? 2000
  const max = opts.backoff?.max ?? 30000
  let backoff = initial
  let socket: WebSocket | null = null
  let connecting = false
  let closed = false
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  const handlers = new Map<string, Set<(msg: never) => void>>()
  const openHandlers = new Set<() => void>()
  const closeHandlers = new Set<() => void>()

  const isOpen = () => socket !== null && socket.readyState === WS.OPEN

  function scheduleReconnect(): void {
    if (closed) return
    if (reconnectTimer !== null) clearTimeout(reconnectTimer)
    reconnectTimer = setTimeout(() => { reconnectTimer = null; void connect() }, backoff)
    backoff = Math.min(backoff * 2, max)
  }

  async function connect(): Promise<void> {
    if (closed || connecting || isOpen()) return
    connecting = true
    let token: string | null
    try {
      token = await opts.getToken()
    } catch {
      token = null
    }
    if (closed) { connecting = false; return }
    if (!token) {
      // Nothing to present: the socket would just be refused. Try again later.
      connecting = false
      scheduleReconnect()
      return
    }
    const ws = new WS(browserSocketUrl(opts.baseUrl ?? ''), ['bearer', token])
    socket = ws
    connecting = false
    ws.onopen = () => {
      backoff = initial
      openHandlers.forEach(h => {
        try { h() } catch { /* a handler's error is its own */ }
      })
    }
    ws.onmessage = (event: MessageEvent) => {
      let msg: { type?: unknown }
      try {
        msg = JSON.parse(event.data as string) as { type?: unknown }
      } catch {
        return
      }
      if (typeof msg.type !== 'string') return
      handlers.get(msg.type)?.forEach(h => h(msg as never))
    }
    ws.onclose = () => {
      if (socket !== ws) return
      socket = null
      closeHandlers.forEach(h => {
        try { h() } catch { /* ditto */ }
      })
      scheduleReconnect()
    }
    ws.onerror = () => {
      // onclose follows; reconnection is handled there.
    }
  }

  // Reconnect at once when the tab returns to the foreground: a background tab's socket is
  // suspended and closed by the browser, and waiting out the backoff would leave input dead.
  const onVisible = () => {
    if (document.visibilityState !== 'visible') return
    if (!socket || socket.readyState === WS.CLOSED || socket.readyState === WS.CLOSING) {
      backoff = initial
      void connect()
    }
  }
  const onOnline = () => { backoff = initial; void connect() }
  if (typeof document !== 'undefined') document.addEventListener('visibilitychange', onVisible)
  if (typeof window !== 'undefined') window.addEventListener('online', onOnline)

  void connect()

  return {
    send(msg) {
      if (isOpen()) {
        socket!.send(JSON.stringify(msg))
        return true
      }
      return false
    },
    onMessage(type, handler) {
      if (!handlers.has(type)) handlers.set(type, new Set())
      handlers.get(type)!.add(handler)
      return () => { handlers.get(type)?.delete(handler) }
    },
    onOpen(handler) {
      openHandlers.add(handler)
      if (isOpen()) handler()
      return () => { openHandlers.delete(handler) }
    },
    onClose(handler) {
      closeHandlers.add(handler)
      return () => { closeHandlers.delete(handler) }
    },
    connected: isOpen,
    /** Closes for good: no reconnection. */
    close() {
      closed = true
      if (reconnectTimer !== null) { clearTimeout(reconnectTimer); reconnectTimer = null }
      if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', onVisible)
      if (typeof window !== 'undefined') window.removeEventListener('online', onOnline)
      const ws = socket
      socket = null
      ws?.close()
    },
  }
}
