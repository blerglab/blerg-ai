import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ShellRateGroup, WebSocketShell } from './shellTransport'
vi.mock('./apiFetch', () => ({ apiFetch: vi.fn() }))
const auth = vi.hoisted(() => ({ token: 'fixture-token' }))
vi.mock('./authClient', () => ({ ensureFreshToken: async () => {}, getAccessToken: () => auth.token }))
class Socket {
  static OPEN = 1
  static instances: Socket[] = []
  readyState = 1
  bufferedAmount = 0
  binaryType = ''
  frames: string[] = []
  onopen?: () => void
  onclose?: () => void
  onerror?: () => void
  onmessage?: (e: { data: ArrayBuffer | string }) => void
  closed = false
  url: URL
  protocols: string[]
  constructor(url: URL, protocols: string[]) { this.url = url; this.protocols = protocols; Socket.instances.push(this) }
  send(data: string) { this.frames.push(data) }
  close() { this.closed = true }
}
beforeEach(() => { auth.token = 'fixture-token'; vi.useFakeTimers(); Socket.instances = []; vi.stubGlobal('WebSocket', Socket) })
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })
const info = (id: string) => ({ handle: id, shell_id: id, runtime: 'sandbox' as const, folder: 'project' })
it('shares a bounded input scheduler across shells and keeps control frames independent', async () => {
  const group = new ShellRateGroup(); const first = new WebSocketShell(info('first'), group); const second = new WebSocketShell(info('second'), group)
  await first.reconnect(); await second.reconnect(); Socket.instances.forEach(s => s.onopen?.())
  first.input(new Uint8Array(12000)); second.input(new Uint8Array(12000))
  first.resize(120, 40)
  expect(JSON.parse(Socket.instances[0].frames[0]).type).toBe('resize')
  await vi.advanceTimersByTimeAsync(1040)
  const inputs = Socket.instances.flatMap(s => s.frames.map(f => JSON.parse(f) as { type: string; data?: string })).filter(f => f.type === 'input')
  expect(inputs.length).toBe(4)
  expect(inputs.reduce((n, f) => n + atob(f.data!).length, 0)).toBeLessThanOrEqual(16384)
  expect(Socket.instances[0].protocols).toEqual(['bearer', 'fixture-token'])
  expect(String(Socket.instances[0].url)).not.toContain('fixture-token')
  first.disconnect(); second.disconnect()
})
it('fences old sockets on reconnect and does not retain detached output', async () => {
  const shell = new WebSocketShell(info('one'), new ShellRateGroup()); const out = vi.fn()
  const unsub = shell.subscribe(out, () => {})
  await shell.reconnect(); const old = Socket.instances[0]; old.onopen?.()
  unsub(); old.onmessage?.({ data: new Uint8Array([65]).buffer })
  shell.subscribe(out, () => {}); expect(out).not.toHaveBeenCalled()
  await shell.reconnect(true); Socket.instances[1].onopen?.()
  old.onmessage?.({ data: new Uint8Array([66]).buffer }); expect(out).not.toHaveBeenCalled()
  Socket.instances[1].onmessage?.({ data: new Uint8Array([67]).buffer }); expect(out).toHaveBeenCalledTimes(1)
  expect(String(Socket.instances[1].url)).toContain('takeover=true')
  shell.disconnect()
})
it('closes on oversized output or a bounded-queue overflow', async () => {
  const shell = new WebSocketShell(info('one'), new ShellRateGroup())
  await shell.reconnect(); Socket.instances[0].onopen?.()
  Socket.instances[0].onmessage?.({ data: new Uint8Array(16385).buffer }); expect(Socket.instances[0].closed).toBe(true)
  await shell.reconnect(); Socket.instances[1].onopen?.(); shell.input(new Uint8Array(65537)); expect(Socket.instances[1].closed).toBe(true)
})

it('clears shell delivery when the browser identity changes', async () => {
  const shell = new WebSocketShell(info('one'), new ShellRateGroup()); const output = vi.fn(); const state = vi.fn()
  shell.subscribe(output, state); await shell.reconnect(); Socket.instances[0].onopen?.()
  auth.token = 'different-login'
  Socket.instances[0].onmessage?.({ data: new Uint8Array([65]).buffer })
  expect(output).not.toHaveBeenCalled(); expect(state).toHaveBeenLastCalledWith('closed'); expect(Socket.instances[0].closed).toBe(true)
})

it('treats syncing as control state and refuses input until running resumes', async () => {
 const shell = new WebSocketShell(info('sync'), new ShellRateGroup()); const changed = vi.fn()
 shell.subscribe(() => {}, changed); await shell.reconnect(); const socket = Socket.instances[0]; socket.onopen?.()
 socket.onmessage?.({data: JSON.stringify({type: 'phase', phase: 'syncing'})})
 expect(changed).toHaveBeenLastCalledWith('syncing'); shell.input(new Uint8Array([65])); await vi.advanceTimersByTimeAsync(300)
 expect(socket.frames.some(frame => JSON.parse(frame).type === 'input')).toBe(false)
 socket.onmessage?.({data: JSON.stringify({type: 'phase', phase: 'running'})})
 shell.input(new Uint8Array([66])); await vi.advanceTimersByTimeAsync(300)
 expect(socket.frames.some(frame => JSON.parse(frame).type === 'input')).toBe(true)
 shell.disconnect()
})
