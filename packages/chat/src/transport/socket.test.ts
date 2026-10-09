import { describe, it, expect, vi } from 'vitest'
import { browserSocketUrl, createBrowserSocket } from './socket'

// A WebSocket the test opens, feeds and closes by hand.
class FakeWebSocket {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3
  static instances: FakeWebSocket[] = []
  readyState = FakeWebSocket.CONNECTING
  sent: string[] = []
  onopen: (() => void) | null = null
  onmessage: ((e: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  url: string
  protocols: string[]
  constructor(url: string, protocols: string[]) {
    this.url = url
    this.protocols = protocols
    FakeWebSocket.instances.push(this)
  }
  send(data: string) { this.sent.push(data) }
  close() { this.readyState = FakeWebSocket.CLOSED; this.onclose?.() }
  open() { this.readyState = FakeWebSocket.OPEN; this.onopen?.() }
  feed(msg: object) { this.onmessage?.({ data: JSON.stringify(msg) }) }
  drop() { this.readyState = FakeWebSocket.CLOSED; this.onclose?.() }
}

const flush = () => new Promise(r => setTimeout(r, 0))
const WS = FakeWebSocket as unknown as typeof WebSocket

describe('browserSocketUrl', () => {
  it('turns the runner origin into its websocket URL, and uses the page host without one', () => {
    expect(browserSocketUrl('https://runner.example/')).toBe('wss://runner.example/ws/browser')
    expect(browserSocketUrl('http://localhost:8080')).toBe('ws://localhost:8080/ws/browser')
    expect(browserSocketUrl('')).toBe(`ws://${location.host}/ws/browser`)
  })
})

describe('createBrowserSocket', () => {
  it('connects with the bearer subprotocol, dispatches messages by type and reports open/close', async () => {
    FakeWebSocket.instances = []
    const s = createBrowserSocket({ baseUrl: 'https://r.example', getToken: async () => 'tok', WebSocket: WS, backoff: { initial: 1, max: 2 } })
    await flush()
    const ws = FakeWebSocket.instances[0]
    expect(ws.url).toBe('wss://r.example/ws/browser')
    expect(ws.protocols).toEqual(['bearer', 'tok'])
    expect(s.send({ type: 'unsubscribe_session', session_id: 's0' })).toBe(false) // not open yet
    const opened = vi.fn()
    const closed = vi.fn()
    const got = vi.fn()
    s.onOpen(opened)
    s.onClose!(closed)
    s.onMessage('agent_event', got)
    ws.open()
    expect(opened).toHaveBeenCalledTimes(1)
    expect(s.connected!()).toBe(true)
    expect(s.send({ type: 'subscribe_agent_events', session_id: 's1' })).toBe(true)
    expect(ws.sent).toEqual(['{"type":"subscribe_agent_events","session_id":"s1"}'])
    ws.feed({ type: 'agent_event', session_id: 's1' })
    ws.feed({ type: 'other' })
    expect(got).toHaveBeenCalledTimes(1)
    // A late onOpen fires at once while the socket is open.
    const late = vi.fn()
    s.onOpen(late)
    expect(late).toHaveBeenCalledTimes(1)
    ws.drop()
    expect(closed).toHaveBeenCalledTimes(1)
    expect(s.connected!()).toBe(false)
    // ...and a new socket follows after the backoff, with a fresh token.
    await new Promise(r => setTimeout(r, 10))
    expect(FakeWebSocket.instances).toHaveLength(2)
    FakeWebSocket.instances[1].open()
    expect(opened).toHaveBeenCalledTimes(2)
    s.close()
    await new Promise(r => setTimeout(r, 10))
    expect(FakeWebSocket.instances).toHaveLength(2)
  })

  it('waits for a token rather than opening a socket it has nothing to present to', async () => {
    FakeWebSocket.instances = []
    let token: string | null = null
    const s = createBrowserSocket({ baseUrl: 'https://r.example', getToken: async () => token, WebSocket: WS, backoff: { initial: 1, max: 1 } })
    await flush()
    expect(FakeWebSocket.instances).toHaveLength(0)
    token = 'tok'
    await new Promise(r => setTimeout(r, 10))
    expect(FakeWebSocket.instances).toHaveLength(1)
    s.close()
  })
})
