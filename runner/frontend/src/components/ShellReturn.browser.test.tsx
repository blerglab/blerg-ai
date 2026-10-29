// #281: a shell's terminal outlives the session view that showed it, for the last sessions viewed,
// so coming back to a session shows the terminal at once (no blank surface, no "Connecting…")
// before its inventory answers, without replaying what it already shows. A sign-in change disposes
// every kept terminal; only the last KEPT_TERMINAL_SESSIONS sessions' terminals are kept.
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { page, userEvent } from 'vitest/browser'
const userEventClick = (el: Element) => userEvent.click(el)
import { MemoryRouter } from 'react-router-dom'
import ShellWorkspace from '../test/ShellWorkspaceHarness'
import CommandPaletteProvider from './CommandPalette'
import { KEPT_TERMINAL_SESSIONS, disposeKeptTerminals, keptTerminalSessions } from '../lib/keptTerminals'
import { ShellRateGroup, WebSocketShell, forgetRetainedShells, retainedShellsFor } from '../shellTransport'
import { useShellCapability } from '../hooks/useShellCapability'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeSession } from '../test/fixtures'
import { resetSessionRetentionForTest, sessionShown } from '../lib/sessionRetention'
import '../index.css'

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
const auth = vi.hoisted(() => ({ renewal: async () => true as boolean }))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), ensureFreshToken: () => auth.renewal(), getAccessToken: () => 'fixture-token' }))
vi.mock('../ws', () => import('../test/wsMock'))

class LiveSocket {
  static OPEN = 1
  static instances: LiveSocket[] = []
  static openDelay = 0
  readyState = 1; bufferedAmount = 0; binaryType = ''
  closed = false
  sent: string[] = []
  url: string
  onopen?: () => void
  onmessage?: (event: { data: ArrayBuffer | string }) => void
  onclose?: () => void
  constructor(url: URL | string) { this.url = String(url); LiveSocket.instances.push(this); if (LiveSocket.openDelay) setTimeout(() => this.onopen?.(), LiveSocket.openDelay); else queueMicrotask(() => this.onopen?.()) }
  send(frame: string) { this.sent.push(frame) }
  close() { this.closed = true }
}
const inputs = (socket: LiveSocket) => socket.sent.filter(frame => JSON.parse(frame).type === 'input')
const pending = <T,>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }
const say = (socket: LiveSocket, text: string) => act(() => { socket.onmessage?.({ data: new TextEncoder().encode(text).buffer }) })
/** The server's replay at the start of a socket (#268): its notice, then the kept scrollback. */
const replay = (socket: LiveSocket, text: string) => act(() => {
  const bytes = new TextEncoder().encode(text)
  socket.onmessage?.({ data: JSON.stringify({ type: 'replay', bytes: bytes.byteLength }) })
  socket.onmessage?.({ data: bytes.buffer })
})

/** The fake server: each session lists one shell; an inventory or the capability can be held or refused. */
const server = { held: null as ReturnType<typeof pending<void>> | null, capability: null as ReturnType<typeof pending<void>> | null, refuse: false, inventories: [] as string[] }
beforeEach(() => {
  LiveSocket.instances = []
  LiveSocket.openDelay = 0
  vi.stubGlobal('WebSocket', LiveSocket)
  server.held = null; server.capability = null; server.refuse = false; server.inventories = []
  auth.renewal = async () => true
  resetSessionRetentionForTest()
  apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.endsWith('/shell-capability')) {
      if (server.capability) await server.capability.promise
      return new Response(JSON.stringify({ available: true }))
    }
    const session = /\/api\/sessions\/([^/]+)\/shells$/.exec(url)?.[1]
    if (session && !init?.method) {
      server.inventories.push(session)
      if (server.held) await server.held.promise
      if (server.refuse) return new Response('{}', { status: 500 })
      // Attached: a socket of this page is open on the shell (a kept terminal's), as the server reports it.
      const attached = LiveSocket.instances.some(socket => !socket.closed && socket.url.includes(`/api/shells/h-${session}/socket`))
      return new Response(JSON.stringify([{ handle: `h-${session}`, shell_id: `shell-${session}`, runtime: 'host', folder: 'project', attached }]))
    }
    return new Response('{}', { status: 404 })
  })
})
afterEach(() => { forgetRetainedShells('login'); disposeKeptTerminals(); vi.unstubAllGlobals() })

const view = (session: string) => <MemoryRouter initialEntries={[`/sessions/${session}`]}><CommandPaletteProvider><div style={{ height: '100dvh' }}>
  <ShellWorkspace sessionId={session} accountId="owner"><div className="agent-composer"><textarea aria-label="Composer" /></div></ShellWorkspace>
</div></CommandPaletteProvider></MemoryRouter>
const screenText = () => document.querySelector('.xterm-screen')?.textContent ?? ''
const showShell = async () => fireEvent.click(await screen.findByRole('tab', { name: 'Shell' }))

/** Visits a session: its shell connects and prints `text`. */
async function visit(session: string, text: string) {
  const ui = render(view(session))
  const before = LiveSocket.instances.length
  await waitFor(() => expect(LiveSocket.instances.length).toBe(before + 1))
  await showShell()
  await waitFor(() => expect(document.querySelector('.xterm-screen')).toBeTruthy())
  await say(LiveSocket.instances.at(-1)!, text)
  await waitFor(() => expect(screenText()).toContain(text.split('\r\n')[0]))
  return ui
}

for (const [width, height] of [[390, 844], [3840, 2160]] as const) {
  it(`A→B→A shows A's terminal picture at once, then the server replay replaces it once, at ${width}px`, async () => {
    await page.viewport(width, height)
    ;(await visit('a', 'project $ echo kept-a\r\nkept-a\r\n')).unmount()
    ;(await visit('b', 'project $ echo kept-b\r\n')).unmount()
    server.held = pending<void>()
    const inventoriesBefore = server.inventories.length
    render(view('a'))
    // First paint: the kept terminal is on the page, with what it showed, and no connection line.
    expect(document.querySelector('[data-shell-id="shell-a"] .xterm-screen')).toBeTruthy()
    await new Promise(r => requestAnimationFrame(() => r(null)))
    expect(screenText()).toContain('echo kept-a')
    expect(screen.queryByText('Connecting…')).toBeNull()
    expect(screen.queryByText(/^(Disconnected\.|Controlled in another tab\.)$/)).toBeNull()
    // The socket was closed when A was left: the inventory (the only read) reconnects it, asking the
    // server's replay, whose reset and history replace the picture once.
    expect(server.inventories.length).toBe(inventoriesBefore + 1)
    expect(LiveSocket.instances.some(socket => !socket.closed && socket.url.includes('/api/shells/h-a/socket'))).toBe(false)
    const sockets = LiveSocket.instances.length
    await act(async () => { server.held!.resolve() })
    await waitFor(() => expect(LiveSocket.instances.length).toBe(sockets + 1))
    const fresh = LiveSocket.instances.at(-1)!
    expect(fresh.url).toContain('replay=1')
    await replay(fresh, 'project $ echo kept-a\r\nkept-a\r\nwhile-away\r\n')
    await waitFor(() => expect(screenText()).toContain('while-away'))
    await say(fresh, 'project $ echo after\r\n')
    await waitFor(() => expect(screenText()).toContain('echo after'))
    // The picture was replaced, not added to: nothing shows twice.
    expect(screenText().split('echo kept-a').length - 1).toBe(1)
    expect(screen.queryByText(/^(Disconnected\.|Controlled in another tab\.)$/)).toBeNull()
  })
}

it('A→B→A never shows the connection line while the kept terminal reconnects behind its picture', async () => {
  ;(await visit('a', 'project $ echo kept-a\r\nkept-a\r\n')).unmount()
  ;(await visit('b', 'project $ echo kept-b\r\n')).unmount()
  server.held = pending<void>()
  const flashes: string[] = []
  const watch = new MutationObserver(records => records.forEach(record => record.addedNodes.forEach(node => {
    if (node instanceof HTMLElement && (node.matches('.shell-connection') || node.querySelector('.shell-connection'))) flashes.push(node.textContent || '')
  })))
  watch.observe(document.body, { childList: true, subtree: true })
  try {
    const sockets = LiveSocket.instances.length
    LiveSocket.openDelay = 150
    render(view('a'))
    await act(async () => { server.held!.resolve() })
    await new Promise(r => setTimeout(r, 300))
    await waitFor(() => expect(LiveSocket.instances.length).toBe(sockets + 1))
    await replay(LiveSocket.instances.at(-1)!, 'project $ echo kept-a\r\nkept-a\r\n')
    await say(LiveSocket.instances.at(-1)!, 'project $ echo after\r\n')
    await waitFor(() => expect(screenText()).toContain('echo after'))
  } finally { watch.disconnect() }
  expect(flashes).toEqual([])
})

it('a slow reconnect behind a kept picture still says Connecting…', async () => {
  ;(await visit('a', 'project $ echo kept-a\r\n')).unmount()
  ;(await visit('b', 'project $ echo kept-b\r\n')).unmount()
  LiveSocket.openDelay = 2500
  render(view('a'))
  await waitFor(() => expect(screen.getByText('Connecting…')).toBeTruthy(), { timeout: 2000 })
  expect(screenText()).toContain('echo kept-a')
})

const typed = (socket: LiveSocket) => inputs(socket).map(frame => atob(JSON.parse(frame).data)).join('')

it('what is typed while a kept terminal reconnects quietly is sent once it runs (review P2)', async () => {
  ;(await visit('a', 'project $ echo kept-a\r\n')).unmount()
  ;(await visit('b', 'project $ echo kept-b\r\n')).unmount()
  const sockets = LiveSocket.instances.length
  LiveSocket.openDelay = 400
  render(view('a'))
  await waitFor(() => expect(LiveSocket.instances.length).toBe(sockets + 1))
  const fresh = LiveSocket.instances.at(-1)!
  ;(document.querySelector('[data-shell-id="shell-a"] .xterm-helper-textarea') as HTMLTextAreaElement).focus()
  await userEvent.keyboard('ls')
  expect(fresh.sent).toEqual([])
  await waitFor(() => expect(typed(fresh)).toBe('ls'), { timeout: 2000 })
})

it('a disconnect caused by sending the held keys still shows Disconnected (re-review P2)', async () => {
  ;(await visit('a', 'project $ echo kept-a\r\n')).unmount()
  ;(await visit('b', 'project $ echo kept-b\r\n')).unmount()
  const sockets = LiveSocket.instances.length
  LiveSocket.openDelay = 400
  render(view('a'))
  await waitFor(() => expect(LiveSocket.instances.length).toBe(sockets + 1))
  ;(document.querySelector('[data-shell-id="shell-a"] .xterm-helper-textarea') as HTMLTextAreaElement).focus()
  await userEvent.keyboard('ls')
  // The session's input queue is full when the held keys go out: the shell disconnects.
  const full = vi.spyOn(ShellRateGroup.prototype, 'enqueue').mockImplementation(owner => { owner.disconnect() })
  try {
    await waitFor(() => expect(screen.getByText('Disconnected.')).toBeTruthy(), { timeout: 2000 })
    await new Promise(r => setTimeout(r, 50))
    expect(screen.getByText('Disconnected.')).toBeTruthy()
  } finally { full.mockRestore() }
})

it('what is typed during a quiet reconnect is never sent after the person left (review P2)', async () => {
  ;(await visit('a', 'project $ echo kept-a\r\n')).unmount()
  ;(await visit('b', 'project $ echo kept-b\r\n')).unmount()
  const sockets = LiveSocket.instances.length
  LiveSocket.openDelay = 400
  const ui = render(view('a'))
  await waitFor(() => expect(LiveSocket.instances.length).toBe(sockets + 1))
  const fresh = LiveSocket.instances.at(-1)!
  ;(document.querySelector('[data-shell-id="shell-a"] .xterm-helper-textarea') as HTMLTextAreaElement).focus()
  await userEvent.keyboard('rm')
  ui.unmount()
  await new Promise(r => setTimeout(r, 700))
  expect(typed(fresh)).toBe('')
})

it('a sign-in change disposes every kept terminal', async () => {
  ;(await visit('a', 'project $ echo private\r\n')).unmount()
  expect(keptTerminalSessions()).toEqual(['a'])
  window.dispatchEvent(new Event('blerg-identity-changed'))
  expect(keptTerminalSessions()).toEqual([])
  server.held = pending<void>()
  render(view('a'))
  await new Promise(r => setTimeout(r, 20))
  expect(screenText()).not.toContain('private')
  await act(async () => { server.held!.resolve() })
})

it(`keeps only the last ${KEPT_TERMINAL_SESSIONS} sessions' terminals`, async () => {
  const sessions = Array.from({ length: KEPT_TERMINAL_SESSIONS + 1 }, (_, i) => `s${i}`)
  for (const session of sessions) (await visit(session, `project $ echo ${session}\r\n`)).unmount()
  expect(keptTerminalSessions()).toEqual(sessions.slice(1))
  expect(document.querySelectorAll('.xterm').length).toBe(0)
})

// The production relationship (SessionDetail): enabled and capabilityPending come from useShellCapability.
function CapabilityView({ session }: { session: string }) {
  const capability = useShellCapability(session, 'running')
  return <MemoryRouter initialEntries={[`/sessions/${session}`]}><CommandPaletteProvider><div style={{ height: '100dvh' }}>
    <ShellWorkspace sessionId={session} accountId="owner" enabled={capability === true} capabilityPending={capability === null}><div className="agent-composer"><textarea aria-label="Composer" /></div></ShellWorkspace>
  </div></CommandPaletteProvider></MemoryRouter>
}

it('the session route draws the kept terminal while the capability and the inventory are both still asked (review 3)', async () => {
  const first = render(<CapabilityView session="cap" />)
  await waitFor(() => expect(LiveSocket.instances.length).toBe(1))
  await showShell()
  await say(LiveSocket.instances[0], 'project $ echo before-capability\r\n')
  await waitFor(() => expect(screenText()).toContain('echo before-capability'))
  first.unmount()
  server.capability = pending<void>(); server.held = pending<void>()
  render(<CapabilityView session="cap" />)
  await new Promise(r => setTimeout(r, 30))
  expect(screenText()).toContain('echo before-capability')
  expect(screen.queryByText('Connecting…')).toBeNull()
  await act(async () => { server.capability!.resolve() })
  await new Promise(r => setTimeout(r, 30))
  expect(screenText()).toContain('echo before-capability')
  await act(async () => { server.held!.resolve() })
  // Then the inventory reconnects it on a new socket (the first closed when the view left).
  await waitFor(() => expect(LiveSocket.instances).toHaveLength(2))
  expect(LiveSocket.instances[0].closed).toBe(true)
})

it('a shell reconnected by hand after a failed inventory is released when the view leaves (review 1)', async () => {
  ;(await visit('a', 'project $ echo first\r\n')).unmount()
  server.refuse = true
  const again = render(view('a'))
  await waitFor(() => expect(document.querySelector('[data-shells-restored="true"]')).toBeTruthy())
  await showShell()
  await userEventClick(await screen.findByRole('button', { name: 'Reconnect' }))
  await waitFor(() => expect(LiveSocket.instances.at(-1)!.closed).toBe(false))
  const manual = LiveSocket.instances.at(-1)!
  await new Promise(r => setTimeout(r, 20))
  again.unmount()
  expect(manual.closed).toBe(true)
})

it('leaving closes the socket, so input queued before leaving is never sent (review P1 1)', async () => {
  const ui = await visit('a', 'project $ echo queued\r\n')
  const socket = LiveSocket.instances.at(-1)!
  const transport = retainedShellsFor('a')[0]
  // Use the input allowance, then queue a command and an automatic reply behind it.
  transport.input(new Uint8Array(12 << 10).fill(0x61), true)
  transport.input(new TextEncoder().encode('echo LATE-COMMAND\r'), true)
  transport.input(new TextEncoder().encode('\x1b[2;1R'), false)
  const before = socket.sent.length
  ui.unmount()
  expect(socket.closed).toBe(true)
  await new Promise(r => setTimeout(r, 1200))
  const late = socket.sent.slice(before).map(frame => JSON.parse(frame) as { type: string; data?: string }).filter(frame => frame.type === 'input').map(frame => atob(frame.data ?? ''))
  expect(late.some(text => text.includes('LATE-COMMAND') || text.includes('[2;1R'))).toBe(false)
  expect(keptTerminalSessions()).toContain('a')
})

it('a second tab attaches without taking over once the first tab left the session', async () => {
  ;(await visit('a', 'project $ echo first-tab\r\n')).unmount()
  // This tab holds no socket on the shell any more (its picture is kept, not its attachment)...
  expect(keptTerminalSessions()).toContain('a')
  expect(LiveSocket.instances.filter(socket => socket.url.includes('/api/shells/h-a/socket')).every(socket => socket.closed)).toBe(true)
  // ...so the server lists it free, and another tab's transport attaches without taking over.
  const listing = await (await apiFetch('/api/sessions/a/shells')).json() as { attached: boolean }[]
  expect(listing[0].attached).toBe(false)
  const other = new WebSocketShell({ handle: 'h-a', shell_id: 'shell-a', runtime: 'host', folder: 'project' }, new ShellRateGroup())
  await act(async () => { await other.reconnect() })
  expect(LiveSocket.instances.at(-1)!.url).not.toContain('takeover')
  other.disconnect()
})

it('a sign-out while a return is reconnecting creates no socket (review P1 2)', async () => {
  ;(await visit('a', 'project $ echo before\r\n')).unmount()
  const renewal = pending<boolean>()
  auth.renewal = () => renewal.promise
  const sockets = LiveSocket.instances.length
  render(view('a'))
  await waitFor(() => expect(server.inventories.filter(x => x === 'a').length).toBe(2))
  await new Promise(r => setTimeout(r, 30))
  window.dispatchEvent(new Event('blerg-signed-out'))
  await act(async () => { renewal.resolve(true); await renewal.promise })
  await new Promise(r => setTimeout(r, 50))
  expect(LiveSocket.instances.length).toBe(sockets)
})

it('a kept terminal never answers a terminal query while detached, and its eviction closes its connection (review 1)', async () => {
  ;(await visit('s0', 'project $ echo zero\r\n')).unmount()
  // Something connects s0's kept transport while no view shows it.
  const transport = retainedShellsFor('s0')[0]
  await act(async () => { await transport.reconnect() })
  const detached = LiveSocket.instances.at(-1)!
  await waitFor(() => expect(detached.closed).toBe(false))
  await say(detached, '\x1b[6n')
  await new Promise(r => setTimeout(r, 50))
  expect(inputs(detached)).toEqual([])
  for (const session of ['s1', 's2', 's3']) (await visit(session, `project $ echo ${session}\r\n`)).unmount()
  expect(keptTerminalSessions()).not.toContain('s0')
  expect(detached.closed).toBe(true)
})

it("a session that ended forgets its kept transports and scrollback, not only its terminal (review 7)", async () => {
  useSessionStore.setState({ sessions: [makeSession({ id: 'gone', status: 'idle' })] })
  const ui = render(view('gone'))
  const leave = sessionShown('fixture', 'gone')
  await waitFor(() => expect(LiveSocket.instances.length).toBe(1))
  await showShell()
  await say(LiveSocket.instances[0], 'PRIVATE-SCROLLBACK\r\n')
  await waitFor(() => expect(screenText()).toContain('PRIVATE-SCROLLBACK'))
  ui.unmount(); leave()
  expect(retainedShellsFor('gone')).toHaveLength(1)
  act(() => useSessionStore.setState({ sessions: [makeSession({ id: 'gone', status: 'ended' })] }))
  await Promise.resolve()
  expect(retainedShellsFor('gone')).toHaveLength(0)
  expect(keptTerminalSessions()).not.toContain('gone')
})

it('a refresh (a new page: nothing kept) gets the server replay on a new socket, after a reset, shown once (#268)', async () => {
  ;(await visit('a', 'project $ echo before-refresh\r\n')).unmount()
  await Promise.resolve()
  // What a reload loses: the kept terminals, the kept transports and the page's sockets.
  disposeKeptTerminals(); forgetRetainedShells('login')
  LiveSocket.instances.forEach(socket => { socket.closed = true })
  const sockets = LiveSocket.instances.length
  render(view('a'))
  await waitFor(() => expect(LiveSocket.instances.length).toBe(sockets + 1))
  const fresh = LiveSocket.instances.at(-1)!
  expect(fresh.url).toContain('replay=1')
  await showShell()
  await replay(fresh, 'project $ echo before-refresh\r\n')
  await waitFor(() => expect(screenText()).toContain('echo before-refresh'))
  await say(fresh, 'project $ echo live\r\n')
  await waitFor(() => expect(screenText()).toContain('echo live'))
  expect(screenText().split('echo before-refresh').length - 1).toBe(1)
})

it('a return after eviction gets the server replay on a new socket, reset first, with nothing shown twice (#268)', async () => {
  ;(await visit('a', 'project $ echo before-eviction\r\n')).unmount()
  for (const session of ['s1', 's2', 's3']) (await visit(session, `project $ echo ${session}\r\n`)).unmount()
  expect(keptTerminalSessions()).not.toContain('a')
  const sockets = LiveSocket.instances.length
  render(view('a'))
  await waitFor(() => expect(LiveSocket.instances.length).toBe(sockets + 1))
  const fresh = LiveSocket.instances.at(-1)!
  expect(fresh.url).toContain('replay=1')
  await showShell()
  // The page's kept scrollback first (a fresh terminal), then the server's replay supersedes it.
  // The replayed history holds a terminal query: it is never answered.
  await replay(fresh, 'project $ echo before-eviction\r\nwhile-away\r\n\x1b[6n')
  await waitFor(() => expect(screenText()).toContain('while-away'))
  expect(screenText().split('echo before-eviction').length - 1).toBe(1)
  await new Promise(r => setTimeout(r, 50))
  expect(inputs(fresh)).toEqual([])
})
