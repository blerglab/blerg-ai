// #281 merge review P1 2: a shell open whose POST is still out when the sign-in or desktop
// authority changes never keeps a transport nor opens a socket, even if the login comes back.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('./apiFetch', () => ({ apiFetch }))
const who = vi.hoisted(() => ({ token: 'login-a' as string | null, renewal: async () => true as boolean }))
vi.mock('./authClient', () => ({ getAccessToken: () => who.token, ensureFreshToken: () => who.renewal() }))
import { ShellRateGroup, forgetRetainedShells, openShell, retainedShellsFor, WebSocketShell } from './shellTransport'

const sockets: unknown[] = []
class FakeSocket { static OPEN = 1; readyState = 1; bufferedAmount = 0; binaryType = ''; onopen?: () => void; constructor() { sockets.push(this); queueMicrotask(() => this.onopen?.()) } send() {} close() {} }
const info = { handle: 'h', shell_id: 's', runtime: 'host', folder: 'project' }
function heldPost() {
  let resolve!: (r: Response) => void
  apiFetch.mockImplementationOnce(() => new Promise<Response>(r => { resolve = r }))
  return () => resolve(new Response(JSON.stringify(info)))
}
beforeEach(() => { vi.useRealTimers(); sockets.length = 0; who.token = 'login-a'; who.renewal = async () => true; vi.stubGlobal('WebSocket', FakeSocket) })
afterEach(() => { forgetRetainedShells('login'); vi.unstubAllGlobals(); apiFetch.mockReset() })

it.each([
  ['a confirmed sign-out', () => window.dispatchEvent(new Event('blerg-signed-out'))],
  ['a desktop scope change', () => window.dispatchEvent(new Event('blerg-desktop-scope-changed'))],
  ['a login changing away and back', () => { who.token = 'login-b'; window.dispatchEvent(new Event('blerg-login-changed')); who.token = 'login-a'; window.dispatchEvent(new Event('blerg-login-changed')) }],
])('%s while the open is pending keeps nothing and opens no socket', async (_, change) => {
  const answer = heldPost()
  const opening = openShell('session', new ShellRateGroup())
  await Promise.resolve()
  change()
  answer()
  await expect(opening).rejects.toThrow(/sign-in changed/)
  await new Promise(r => setTimeout(r, 10))
  expect(sockets).toHaveLength(0)
  expect(retainedShellsFor('session')).toHaveLength(0)
})

it('a failed renewal opens no socket', async () => {
  who.renewal = async () => false
  const shell = new WebSocketShell(info as never, new ShellRateGroup())
  await shell.reconnect()
  expect(sockets).toHaveLength(0)
})

it('an open with nothing changing still connects', async () => {
  const answer = heldPost()
  const opening = openShell('session', new ShellRateGroup())
  answer()
  await opening
  expect(sockets).toHaveLength(1)
})
