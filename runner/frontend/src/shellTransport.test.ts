import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { disconnectOwned, openShell, ShellRateGroup, WebSocketShell } from './shellTransport'
vi.mock('./apiFetch', () => ({ apiFetch: vi.fn() }))
const auth = vi.hoisted(() => ({ token: 'fixture-token', renewal: async () => true as boolean }))
vi.mock('./authClient', () => ({ ensureFreshToken: () => auth.renewal(), getAccessToken: () => auth.token }))
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
beforeEach(() => { auth.token = 'fixture-token'; auth.renewal = async () => true; vi.useFakeTimers(); Socket.instances = []; vi.stubGlobal('WebSocket', Socket) })
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })
const info = (id: string) => ({ handle: id, shell_id: id, runtime: 'sandbox' as const, folder: 'project' })
it('shares a bounded input scheduler across shells and keeps control frames independent', async () => {
  const group = new ShellRateGroup(); const first = new WebSocketShell(info('first'), group); const second = new WebSocketShell(info('second'), group)
  await first.reconnect(); await second.reconnect(); Socket.instances.forEach(s => s.onopen?.())
  first.input(new Uint8Array(12000)); second.input(new Uint8Array(12000))
  first.resize(120, 40)
  expect(Socket.instances[0].frames.some(f => JSON.parse(f).type === 'resize')).toBe(true)
  const inputs = () => Socket.instances.flatMap(s => s.frames.map(f => JSON.parse(f) as { type: string; data?: string })).filter(f => f.type === 'input')
  const bytes = () => inputs().reduce((n, f) => n + atob(f.data!).length, 0)
  // The first shell's 12000 bytes fit the window and go out at once; the second waits a second.
  expect(bytes()).toBe(12000)
  await vi.advanceTimersByTimeAsync(990)
  expect(bytes()).toBe(12000)
  await vi.advanceTimersByTimeAsync(50)
  expect(bytes()).toBe(24000)
  expect(Socket.instances[0].protocols).toEqual(['bearer', 'fixture-token'])
  expect(String(Socket.instances[0].url)).not.toContain('fixture-token')
  first.disconnect(); second.disconnect()
})
it('sends keystrokes at once and coalesces input queued past the budget', async () => {
  const shell = new WebSocketShell(info('keys'), new ShellRateGroup())
  await shell.reconnect(); const socket = Socket.instances[0]; socket.onopen?.()
  const inputs = () => socket.frames.map(f => JSON.parse(f) as { type: string; data?: string }).filter(f => f.type === 'input')
  for (let i = 0; i < 20; i++) shell.input(new Uint8Array([97 + i]), true)
  expect(inputs().length).toBe(20)
  // Fill the byte budget, then type: the queued keys leave as one frame once the window frees.
  shell.input(new Uint8Array(12268))
  for (let i = 0; i < 5; i++) shell.input(new Uint8Array([65 + i]), true)
  const before = inputs().length
  await vi.advanceTimersByTimeAsync(1000)
  const after = inputs().slice(before)
  expect(after.length).toBe(1)
  expect(atob(after[0].data!)).toBe('ABCDE')
  shell.disconnect()
})
it('never sends more than the frame budget in a second', async () => {
  const shell = new WebSocketShell(info('frames'), new ShellRateGroup())
  await shell.reconnect(); const socket = Socket.instances[0]; socket.onopen?.()
  const count = () => socket.frames.filter(f => JSON.parse(f).type === 'input').length
  for (let i = 0; i < 100; i++) shell.input(new Uint8Array([120]), true)
  expect(count()).toBe(64)
  await vi.advanceTimersByTimeAsync(1000)
  expect(count()).toBe(65)
  shell.disconnect()
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

it.each([
 ['runtime_consent_refused', 'Consent no longer matches this launch. Open the shell again to review the current machine and account.'],
 ['runtime_start_refused', 'The desktop could not start the contained shell. Check the desktop daemon log, then reconnect the desktop and try again.'],
 ['runtime_binding_refused', 'The session runtime changed. Reconnect the desktop, then open the shell again.'],
 ['runtime_fenced', 'The shell is paused after a failed start or while the session recovers. Wait a few seconds and try again. If it keeps failing, reconnect the desktop or resume the session.'],
 ['runtime_busy', 'The desktop is busy opening other session workspaces. Try again in a moment.'],
 ['untrusted-private-error', 'Shell is unavailable. Check the desktop daemon log, then reconnect the desktop and try again.'],
])('explains %s without exposing runtime text', async (code, message) => {
 const { apiFetch } = await import('./apiFetch')
 vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify({error:'private-upstream-text',code}),{status:403}))
 await expect(openShell('session',new ShellRateGroup())).rejects.toThrow(message)
})

it.each(['null', '<html>upstream unavailable</html>', '{"code":{"private":"text"}}'])('uses a safe fallback for malformed runtime replies', async body => {
 const { apiFetch } = await import('./apiFetch')
 vi.mocked(apiFetch).mockResolvedValueOnce(new Response(body,{status:503}))
 await expect(openShell('session',new ShellRateGroup())).rejects.toThrow('Shell is unavailable. Check the desktop daemon log, then reconnect the desktop and try again.')
})

it('shows only a fixed safe cause for a failed shell start', async () => {
 const { apiFetch } = await import('./apiFetch')
 vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify({code:'runtime_start_refused',cause:'helper_missing',error:'Bearer fixture-secret'}),{status:403}))
 await expect(openShell('session',new ShellRateGroup())).rejects.toThrow('A shell executable or its working directory is missing.')
 vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify({code:'runtime_start_refused',cause:'Bearer fixture-secret'}),{status:403}))
 await expect(openShell('session',new ShellRateGroup())).rejects.toThrow('The desktop could not start the contained shell.')
})

it.each([
 ['helper_missing', 'A shell executable or its working directory is missing.'],
 ['permission_denied', 'Permission denied starting the shell.'],
 ['start_timeout', 'Shell startup timed out.'],
 ['start_unavailable', 'The shell could not start.'],
])('describes %s without assuming the helper failed', async (cause, message) => {
 const { apiFetch } = await import('./apiFetch')
 vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify({code:'runtime_start_refused',cause}),{status:403}))
 await expect(openShell('session',new ShellRateGroup())).rejects.toThrow(message)
})

const stageMessages = [
 ['helper_start', 'The shell helper could not be launched. Check the service process permissions and restrictions.'],
 ['workspace_admission', 'The session workspace could not authorize this shell launch. Reconnect the desktop.'],
 ['bootstrap_read', 'The shell helper could not read its launch request.'],
 ['bootstrap_validate', 'The shell helper rejected its launch request.'],
 ['resolve_root', 'The shell project directory could not be resolved.'],
 ['resolve_identity', 'The shell account could not be resolved.'],
 ['verify_identity', 'The shell account changed. Review host shell consent again.'],
 ['verify_cwd', 'The shell working directory changed.'],
 ['verify_root', 'The shell project directory changed.'],
 ['verify_grant', 'Shell authorization could not be verified. Reconnect the desktop.'],
 ['open_root', 'The shell project directory could not be opened.'],
 ['open_cwd', 'The shell working directory could not be opened.'],
 ['verify_uid', 'The shell account ID is unsupported.'],
 ['pty_open', "The shell terminal could not be opened. Check the service's terminal access."],
 ['exec_shell', 'The account shell could not be executed. Check its permissions and service restrictions.'],
 ['pty_configure', 'The shell terminal could not be configured.'],
 ['containment_setup', "Shell containment could not be established. Check the service's tracing and seccomp restrictions."],
 ['lease_create', 'The shell authorization expired during startup. Try opening the shell again.'],
 ['helper_ready', 'The shell helper could not confirm startup.'],
 ['helper_stopped', 'The shell helper stopped.'],
 ['helper_input_pipe', "The shell helper's input channel could not be opened."],
 ['helper_output_pipe', "The shell helper's output channel could not be opened."],
 ['helper_record', "The shell helper's ownership could not be recorded."],
 ['helper_write', "The shell helper's request could not be sent."],
 ['helper_wait', 'The shell helper did not acknowledge the request before it was canceled or timed out.'],
 ['helper_reply', 'The shell helper rejected a control request.'],
 ['admit_shell', 'The shell launch is paused or awaiting recovery.'],
 ['verify_binding', 'The shell launch changed. Reconnect the desktop and review consent again.'],
 ['verify_reservation', 'The shell launch reservation is no longer valid.'],
 ['verify_policy', 'Shell authorization changed. Reconnect the desktop.'],
 ['grant_clock', "This desktop's clock is off by more than a few seconds. Sync it (on WSL: sudo hwclock -s), then try again."],
 ['cleanup_shell', 'Shell cleanup could not be verified. Check the desktop daemon log.'],
 ['control_read', "The shell helper's control channel closed."],
 ['control_write', 'The shell helper could not acknowledge a control request.'],
 ['control_grant', 'The shell helper rejected control authorization.'],
 ['lease_check', 'The shell authorization expired or was revoked.'],
 ['input_sequence', 'The shell helper rejected the input sequence.'],
 ['resize_sequence', 'The shell helper rejected the terminal resize sequence.'],
 ['control_operation', 'The shell helper rejected a control operation.'],
 ['control_apply', 'The shell helper could not apply a control operation.'],
]

it.each(stageMessages)('shows the fixed sentence for stage %s', async (cause, message) => {
 const { apiFetch } = await import('./apiFetch')
 vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify({code:'runtime_start_refused',cause,error:'Bearer fixture-secret'}),{status:403}))
 await expect(openShell('session',new ShellRateGroup())).rejects.toThrow(message)
})

it.each(stageMessages)('delivers live stage %s to the error subscriber', async (cause, message) => {
 const shell = new WebSocketShell(info('live'), new ShellRateGroup())
 const failed = vi.fn(); const output = vi.fn()
 const unsub = shell.subscribe(output, () => {}, failed)
 await shell.reconnect(); const socket = Socket.instances[0]; socket.onopen?.()
 socket.onmessage?.({data:JSON.stringify({type:'error',cause,error:'Bearer fixture-secret'})})
 expect(failed).toHaveBeenCalledExactlyOnceWith(message)
 expect(output).not.toHaveBeenCalled(); expect(socket.closed).toBe(true)
 unsub()
 const restored = vi.fn(); shell.subscribe(output, () => {}, restored)
 expect(restored).toHaveBeenCalledExactlyOnceWith(message)
 await shell.reconnect()
 socket.onmessage?.({data:JSON.stringify({type:'error',cause:'lease_check'})})
 expect(restored).toHaveBeenCalledTimes(1)
 shell.disconnect()
})

it.each(['Bearer fixture-secret\nforged', '__proto__', 'constructor', null, {stage:'input_sequence'}])('redacts an untrusted live cause %s', async cause => {
 const shell = new WebSocketShell(info('live'), new ShellRateGroup()); const failed = vi.fn()
 shell.subscribe(() => {}, () => {}, failed)
 await shell.reconnect(); const socket = Socket.instances[0]; socket.onopen?.()
 socket.onmessage?.({data:JSON.stringify({type:'error',cause,error:'Bearer fixture-secret'})})
 expect(failed).toHaveBeenCalledExactlyOnceWith('The shell stopped. Check the desktop daemon log, then open a new shell.')
})

it('suppresses live failure diagnostics after login changes', async () => {
 const shell = new WebSocketShell(info('live'), new ShellRateGroup()); const failed = vi.fn()
 shell.subscribe(() => {}, () => {}, failed); await shell.reconnect()
 auth.token = 'another-login'
 Socket.instances[0].onmessage?.({data:JSON.stringify({type:'error',cause:'input_sequence'})})
 expect(failed).not.toHaveBeenCalled()
})
const bytesOf = (text: string) => new Uint8Array([...text].map(c => c.charCodeAt(0))).buffer
it('asks for the server scrollback and marks exactly its bytes as replay', async () => {
  const shell = new WebSocketShell(info('replay'), new ShellRateGroup()); const out: [string, boolean][] = []
  shell.subscribe((bytes, replay) => out.push([new TextDecoder().decode(bytes), !!replay]), () => {})
  await shell.reconnect(); const socket = Socket.instances[0]; socket.onopen?.()
  expect(new URL(String(socket.url)).searchParams.get('replay')).toBe('1')
  socket.onmessage?.({ data: JSON.stringify({ type: 'replay', bytes: 9, seq: 120 }) })
  socket.onmessage?.({ data: bytesOf('kept ') })
  socket.onmessage?.({ data: bytesOf('tail') })
  socket.onmessage?.({ data: bytesOf('live') })
  // A terminal reset first, so the server's history supersedes what this page showed.
  expect(out).toEqual([['\x1bc', true], ['kept ', true], ['tail', true], ['live', false]])
  expect(socket.closed).toBe(false)
  shell.disconnect()
})
it('closes a socket whose replay is oversized, repeated, or overruns its notice', async () => {
  for (const frames of [
    [JSON.stringify({ type: 'replay', bytes: (256 << 10) + 1 })],
    [JSON.stringify({ type: 'replay', bytes: 4 }), JSON.stringify({ type: 'replay', bytes: 4 })],
    [JSON.stringify({ type: 'replay', bytes: 2 }), new Uint8Array(3).buffer],
  ]) {
    Socket.instances = []
    const shell = new WebSocketShell(info('bad-replay'), new ShellRateGroup())
    shell.subscribe(() => {}, () => {})
    await shell.reconnect(); const socket = Socket.instances[0]; socket.onopen?.()
    for (const data of frames) socket.onmessage?.({ data })
    expect(socket.closed).toBe(true)
  }
})

it('does not attach a stale restore after its list is canceled', async () => {
  const { apiFetch } = await import('./apiFetch')
  const { restoreShells } = await import('./shellTransport')
  const abort = new AbortController()
  vi.mocked(apiFetch).mockImplementationOnce(async () => { abort.abort(); return new Response(JSON.stringify([info('stale')])) })
  expect(await restoreShells('session', new ShellRateGroup(), abort.signal)).toEqual([])
  expect(Socket.instances).toHaveLength(0)
})
it('does not open a socket when restoration is canceled during token refresh', async () => {
  const abort = new AbortController()
  const shell = new WebSocketShell(info('canceled'), new ShellRateGroup())
  const connecting = shell.reconnect(false, abort.signal)
  abort.abort()
  await connecting
  expect(Socket.instances).toHaveLength(0)
})
it('reuses this page’s running transport when inventory marks its shell attached', async () => {
  const { apiFetch } = await import('./apiFetch')
  const { restoreShells } = await import('./shellTransport')
  const group = new ShellRateGroup(), owned = new WebSocketShell(info('owned'), group)
  await owned.reconnect(); Socket.instances[0].onopen?.()
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify([{ ...info('owned'), attached: true }])))
  expect(await restoreShells('session', group, undefined, [owned])).toEqual([owned])
  expect(Socket.instances).toHaveLength(1)
  expect(Socket.instances[0].closed).toBe(false)
  owned.disconnect()
})
it('leaves a shell owned by another browser disconnected until an explicit takeover', async () => {
  const { apiFetch } = await import('./apiFetch')
  const { restoreShells } = await import('./shellTransport')
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify([{ ...info('elsewhere'), attached: true }])))
  const [shell] = await restoreShells('session', new ShellRateGroup())
  const state = vi.fn()
  shell.subscribe(() => {}, state)
  expect(state).toHaveBeenLastCalledWith('disconnected')
  expect(Socket.instances).toHaveLength(0)
  await shell.reconnect(true)
  expect(Socket.instances[0].url.searchParams.get('takeover')).toBe('true')
  shell.disconnect()
})

it('names the inactivity limit that ended the shell and closes', async () => {
  for (const [reason, after, text] of [['idle', 1800, 'Shell ended after 30 minutes without typing.'], ['detached', 36000, 'Shell ended after 10 hours with no browser attached.'], ['detached', 5400, 'Shell ended after 1 hour 30 minutes with no browser attached.']] as const) {
    Socket.instances = []
    const shell = new WebSocketShell(info(reason), new ShellRateGroup()); const state = vi.fn(); const failed = vi.fn()
    shell.subscribe(() => {}, state, failed); await shell.reconnect(); const socket = Socket.instances[0]; socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({ type: 'ended', reason, after_seconds: after }) })
    expect(state).toHaveBeenLastCalledWith('closed'); expect(socket.closed).toBe(true)
    expect(shell.endedMessage()).toBe(text); expect(failed).not.toHaveBeenCalled()
  }
  // Any other reason is not a cause the tab may show: the frame is invalid control and only disconnects.
  Socket.instances = []
  const shell = new WebSocketShell(info('other'), new ShellRateGroup()); const state = vi.fn()
  shell.subscribe(() => {}, state); await shell.reconnect(); Socket.instances[0].onopen?.()
  Socket.instances[0].onmessage?.({ data: JSON.stringify({ type: 'ended', reason: 'revoked', after_seconds: 1 }) })
  expect(state).toHaveBeenLastCalledWith('disconnected'); expect(shell.endedMessage()).toBe('')
})

const text = (value: string) => new Uint8Array([...value].map(c => c.charCodeAt(0))).buffer
// Card #257: leaving a session unmounts its view. Returning must reattach the same
// shell and show what the person saw before leaving, never output nobody saw.
it('reattaches a returning session view to its shell with the scrollback it displayed', async () => {
  const { apiFetch } = await import('./apiFetch')
  const { restoreShells } = await import('./shellTransport')
  const inventory = () => new Response(JSON.stringify([{ ...info('kept'), attached: false }]))
  vi.mocked(apiFetch).mockResolvedValueOnce(inventory())
  const first = new ShellRateGroup()
  const [shell] = await restoreShells('return-session', first)
  const seen: string[] = []
  const unsubscribe = shell.subscribe(bytes => seen.push(new TextDecoder().decode(bytes)), () => {})
  const socket = Socket.instances[0]; socket.onopen?.()
  socket.onmessage?.({ data: text('before leaving') })
  // Leave: the view unsubscribes and disconnects, and nothing ends the shell.
  unsubscribe(); shell.disconnect()
  socket.onmessage?.({ data: text('never shown') })
  expect(apiFetch).not.toHaveBeenCalledWith(expect.stringContaining('/api/shells/'), expect.objectContaining({ method: 'DELETE' }))
  // Return: a new view and a new rate group inventory the session again.
  vi.mocked(apiFetch).mockResolvedValueOnce(inventory())
  const second = new ShellRateGroup()
  const [returned] = await restoreShells('return-session', second)
  expect(returned).toBe(shell)
  expect(Socket.instances).toHaveLength(2)
  const replay: string[] = []
  returned.subscribe(bytes => replay.push(new TextDecoder().decode(bytes)), () => {})
  expect(replay).toEqual(['before leaving'])
  Socket.instances[1].onopen?.()
  returned.input(new Uint8Array([97]), true)
  expect(Socket.instances[1].frames.some(f => JSON.parse(f).type === 'input')).toBe(true)
  returned.disconnect()
})
it('keeps no scrollback across a sign-in change, an ended shell or an inventory that dropped it', async () => {
  const { apiFetch } = await import('./apiFetch')
  const { restoreShells } = await import('./shellTransport')
  const listed = (id: string) => new Response(JSON.stringify([{ ...info(id), attached: false }]))
  const show = async (session: string, id: string) => {
    vi.mocked(apiFetch).mockResolvedValueOnce(listed(id))
    const [shell] = await restoreShells(session, new ShellRateGroup())
    const unsubscribe = shell.subscribe(() => {}, () => {})
    const socket = Socket.instances.at(-1)!; socket.onopen?.()
    socket.onmessage?.({ data: text('private') })
    unsubscribe(); shell.disconnect()
    return shell
  }
  const replayed = (shell: { subscribe: WebSocketShell['subscribe'] }) => { const out = vi.fn(); shell.subscribe(out, () => {}); return out }
  // Another sign-in on this page gets a fresh transport and nothing displayed to the last one.
  const before = await show('identity-session', 'identity')
  auth.token = 'other-login'
  vi.mocked(apiFetch).mockResolvedValueOnce(listed('identity'))
  const [other] = await restoreShells('identity-session', new ShellRateGroup())
  expect(other).not.toBe(before)
  expect(replayed(other)).not.toHaveBeenCalled()
  other.disconnect(); auth.token = 'fixture-token'
  // An ended shell forgets what it showed.
  const ended = await show('ended-session', 'ended')
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response(null, { status: 204 }))
  await ended.end()
  expect(replayed(ended)).not.toHaveBeenCalled()
  // A shell the session no longer lists is forgotten too.
  const dropped = await show('dropped-session', 'dropped')
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response('[]'))
  expect(await restoreShells('dropped-session', new ShellRateGroup())).toEqual([])
  expect(replayed(dropped)).not.toHaveBeenCalled()
})

// #257 review: retained transports are shared between a view that left and the view
// that returned. Each restore holds a lease (its view's rate group); a stale restore
// or cleanup must never disconnect the newer owner.
const keptShell = async (session: string, id: string, shown = 'shown before leaving') => {
  const { apiFetch } = await import('./apiFetch')
  const { restoreShells } = await import('./shellTransport')
  const group = new ShellRateGroup()
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify([{ ...info(id), attached: false }])))
  const [shell] = await restoreShells(session, group)
  const unsubscribe = shell.subscribe(() => {}, () => {})
  const socket = Socket.instances.at(-1)!; socket.onopen?.()
  socket.onmessage?.({ data: text(shown) })
  // Leave: the view that owns it unsubscribes and disconnects.
  unsubscribe(); shell.disconnect()
  return shell as WebSocketShell
}
const replayOf = (shell: { subscribe: WebSocketShell['subscribe'] }) => { const seen: string[] = []; const off = shell.subscribe(bytes => seen.push(new TextDecoder().decode(bytes)), () => {}); off(); return seen }
const deferred = () => { let resolve!: (v: boolean) => void; const promise = new Promise<boolean>(r => { resolve = r }); return { promise, resolve } }
it('keeps a returning view connected when a canceled restore shares its token renewal', async () => {
  const { apiFetch } = await import('./apiFetch')
  const { restoreShells, disconnectOwned } = await import('./shellTransport')
  const shell = await keptShell('rapid', 'rapid')
  const sockets = Socket.instances.length
  const renewal = deferred(); auth.renewal = () => renewal.promise
  const listed = () => new Response(JSON.stringify([{ ...info('rapid'), attached: false }]))
  // Return (view A), leave again before renewal completes, and return once more (view B).
  const groupA = new ShellRateGroup(), abortA = new AbortController()
  vi.mocked(apiFetch).mockResolvedValueOnce(listed())
  const a = restoreShells('rapid', groupA, abortA.signal)
  await vi.advanceTimersByTimeAsync(0)
  abortA.abort()
  const groupB = new ShellRateGroup()
  vi.mocked(apiFetch).mockResolvedValueOnce(listed())
  const b = restoreShells('rapid', groupB)
  await vi.advanceTimersByTimeAsync(0)
  renewal.resolve(true)
  const [restoredA, restoredB] = await Promise.all([a, b])
  expect(restoredB).toEqual([shell])
  expect(Socket.instances.length).toBe(sockets + 1)
  const live = Socket.instances.at(-1)!
  expect(live.closed).toBe(false)
  // View A's late cleanup, as ShellWorkspace runs it for a view that went away.
  restoredA.forEach(p => disconnectOwned(p, groupA))
  disconnectOwned(shell, groupA)
  expect(live.closed).toBe(false)
  live.onopen?.()
  const states: string[] = []
  shell.subscribe(() => {}, s => states.push(s))
  expect(states.at(-1)).toBe('running')
  shell.input(new Uint8Array([97]), true)
  expect(live.frames.some(f => JSON.parse(f).type === 'input')).toBe(true)
  shell.disconnect()
})
it('never closes a newer view’s socket when the canceled restore finishes renewing last', async () => {
  const { apiFetch } = await import('./apiFetch')
  const { restoreShells, disconnectOwned } = await import('./shellTransport')
  const shell = await keptShell('late', 'late')
  const slow = deferred(); auth.renewal = () => slow.promise
  const listed = () => new Response(JSON.stringify([{ ...info('late'), attached: false }]))
  const groupA = new ShellRateGroup(), abortA = new AbortController()
  vi.mocked(apiFetch).mockResolvedValueOnce(listed())
  const a = restoreShells('late', groupA, abortA.signal)
  await vi.advanceTimersByTimeAsync(0)
  abortA.abort()
  // View B renews at once and opens its socket while A is still waiting.
  auth.renewal = async () => true
  vi.mocked(apiFetch).mockResolvedValueOnce(listed())
  expect(await restoreShells('late', new ShellRateGroup())).toEqual([shell])
  const live = Socket.instances.at(-1)!; live.onopen?.()
  slow.resolve(true)
  const restoredA = await a
  expect(live.closed).toBe(false)
  // The old view's cleanups run too: it no longer owns the transport.
  restoredA.forEach(p => disconnectOwned(p, groupA))
  disconnectOwned(shell, groupA)
  expect(live.closed).toBe(false)
  shell.disconnect()
})
// #257 round 2: one mounted view restores twice (a capability refresh restarts its
// effect) while one renewal is pending. Both attempts share the view's rate group, so
// the group cannot be the lease: the abandoned attempt must not close the newer socket.
it('keeps the newer restore of the same view connected when the abandoned one shares its renewal', async () => {
  const { apiFetch } = await import('./apiFetch')
  const { restoreShells, ShellLease } = await import('./shellTransport')
  const shell = await keptShell('same-view', 'same-view')
  const sockets = Socket.instances.length
  const renewal = deferred(); auth.renewal = () => renewal.promise
  const listed = () => new Response(JSON.stringify([{ ...info('same-view'), attached: false }]))
  const group = new ShellRateGroup(), abortA = new AbortController()
  const leaseA = new ShellLease(), leaseB = new ShellLease()
  vi.mocked(apiFetch).mockResolvedValueOnce(listed())
  const a = restoreShells('same-view', group, abortA.signal, [], [], leaseA)
  await vi.advanceTimersByTimeAsync(0)
  abortA.abort()
  vi.mocked(apiFetch).mockResolvedValueOnce(listed())
  const b = restoreShells('same-view', group, undefined, [], [], leaseB)
  await vi.advanceTimersByTimeAsync(0)
  renewal.resolve(true)
  const [restoredA, restoredB] = await Promise.all([a, b])
  expect(restoredB).toEqual([shell])
  expect(Socket.instances.length).toBe(sockets + 1)
  const live = Socket.instances.at(-1)!
  expect(live.closed).toBe(false)
  // The abandoned attempt's inactive-effect cleanup is a no-op too.
  restoredA.forEach(p => disconnectOwned(p, leaseA))
  disconnectOwned(shell, leaseA)
  expect(live.closed).toBe(false)
  // The view itself going away still disconnects what it owns.
  disconnectOwned(shell, group)
  expect(live.closed).toBe(true)
})
// #257 round 2: an open whose renewal is still pending when the person leaves. The
// returning view adopts the same kept transport; the old open's late cleanup must not close it.
it('never lets an abandoned open close the transport a returning view adopted', async () => {
  const { apiFetch } = await import('./apiFetch')
  const { restoreShells, ShellLease } = await import('./shellTransport')
  const slow = deferred()
  let renewals = 0
  auth.renewal = () => renewals++ === 0 ? slow.promise : Promise.resolve(true)
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify(info('pending-open'))))
  const openLease = new ShellLease()
  const opening = openShell('pending-open', new ShellRateGroup(), openLease)
  await vi.advanceTimersByTimeAsync(0)
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify([{ ...info('pending-open'), attached: false }])))
  const [returned] = await restoreShells('pending-open', new ShellRateGroup())
  const live = Socket.instances.at(-1)!; live.onopen?.()
  slow.resolve(true)
  const opened = await opening
  expect(opened).toBe(returned)
  // The view that opened it went away: ShellWorkspace's late cleanup.
  disconnectOwned(opened, openLease)
  expect(live.closed).toBe(false)
  returned.disconnect()
})
const jwt = (claims: object) => `h.${btoa(JSON.stringify(claims)).replace(/=+$/, '')}.s`
it('erases kept scrollback when the account or login session changes, and A→B→A cannot revive it', async () => {
  const { apiFetch } = await import('./apiFetch')
  const { restoreShells } = await import('./shellTransport')
  const accountA = jwt({ kind: 'user', sub: 'a', sid: 'one' }), accountB = jwt({ kind: 'user', sub: 'b', sid: 'two' })
  auth.token = accountA
  const kept = await keptShell('private-session', 'private', 'private screen')
  expect(replayOf(kept)).toEqual(['private screen'])
  // Account B signs in on this page and restores its own (empty) session list; no event needed.
  auth.token = accountB
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response('[]'))
  expect(await restoreShells('other-session', new ShellRateGroup())).toEqual([])
  expect(replayOf(kept)).toEqual([])
  // Back to A: the same handle gets a fresh transport with nothing to replay.
  auth.token = accountA
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify([{ ...info('private'), attached: false }])))
  const [again] = await restoreShells('private-session', new ShellRateGroup())
  expect(again).not.toBe(kept)
  expect(replayOf(again)).toEqual([])
  again.disconnect()
  // The sign-in events clear kept shells centrally, including detached ones, for
  // a new login session of the same account (sid) and a confirmed sign-out.
  for (const event of ['blerg-login-changed', 'blerg-identity-changed', 'blerg-signed-out']) {
    auth.token = accountA
    const shell = await keptShell(`event-${event}`, `event-${event}`, 'private screen')
    const states: string[] = []
    window.dispatchEvent(new Event(event))
    expect(replayOf(shell)).toEqual([])
    shell.subscribe(() => {}, s => states.push(s))
    expect(states.at(-1)).toBe('closed')
    vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify([{ ...info(`event-${event}`), attached: false }])))
    const [fresh] = await restoreShells(`event-${event}`, new ShellRateGroup())
    expect(fresh).not.toBe(shell)
    fresh.disconnect()
  }
})
it('drops detached kept shells when the desktop scope changes', async () => {
  const kept = await keptShell('scope-session', 'scope')
  window.dispatchEvent(new Event('blerg-desktop-scope-changed'))
  expect(replayOf(kept)).toEqual([])
})
it('does not let a restore that started under the old sign-in repopulate kept shells', async () => {
  const { apiFetch } = await import('./apiFetch')
  const { restoreShells } = await import('./shellTransport')
  const accountA = jwt({ kind: 'user', sub: 'a', sid: 'one' })
  auth.token = accountA
  let reply!: (r: Response) => void
  vi.mocked(apiFetch).mockImplementationOnce(() => new Promise<Response>(r => { reply = r }))
  const pending = restoreShells('fenced-session', new ShellRateGroup())
  await vi.advanceTimersByTimeAsync(0)
  window.dispatchEvent(new Event('blerg-login-changed'))
  reply(new Response(JSON.stringify([{ ...info('fenced'), attached: false }])))
  expect(await pending).toEqual([])
  expect(Socket.instances).toHaveLength(0)
  // Same during the socket's token renewal: the transport is forgotten, not kept.
  const renewal = deferred(); auth.renewal = () => renewal.promise
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify([{ ...info('fenced'), attached: false }])))
  const renewing = restoreShells('fenced-session', new ShellRateGroup())
  await vi.advanceTimersByTimeAsync(0)
  window.dispatchEvent(new Event('blerg-login-changed'))
  renewal.resolve(true)
  expect(await renewing).toEqual([])
  auth.renewal = async () => true
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify([{ ...info('fenced'), attached: false }])))
  const [fresh] = await restoreShells('fenced-session', new ShellRateGroup())
  expect(replayOf(fresh)).toEqual([])
  fresh.disconnect()
})
it('marks replayed scrollback as replay and live output as live', async () => {
  const kept = await keptShell('replay-session', 'replay', '\u001b[6n')
  const seen: [string, boolean | undefined][] = []
  kept.subscribe((bytes, replay) => seen.push([new TextDecoder().decode(bytes), replay]), () => {})
  await kept.reconnect(); const socket = Socket.instances.at(-1)!; socket.onopen?.()
  socket.onmessage?.({ data: text('\u001b[6n') })
  expect(seen).toEqual([['\u001b[6n', true], ['\u001b[6n', undefined]])
  kept.disconnect()
})
it('keeps a server replay that arrives before the view subscribes, fully or in part', async () => {
  const text = (out: [string, boolean][]) => out.map(([t, r]) => `${r ? 'R' : 'L'}:${t}`)
  // All of it before the subscriber.
  let shell = new WebSocketShell(info('early-all'), new ShellRateGroup())
  await shell.reconnect(); let socket = Socket.instances[0]; socket.onopen?.()
  socket.onmessage?.({ data: JSON.stringify({ type: 'replay', bytes: 20 }) })
  socket.onmessage?.({ data: bytesOf('PRIVATE-SCROLLBACK\r\n') })
  let out: [string, boolean][] = []
  shell.subscribe((bytes, replay) => out.push([new TextDecoder().decode(bytes), !!replay]), () => {})
  socket.onmessage?.({ data: bytesOf('LIVE') })
  expect(text(out)).toEqual(['R:\x1bc', 'R:PRIVATE-SCROLLBACK\r\n', 'L:LIVE'])
  shell.disconnect()
  // The subscriber arrives between two replay frames.
  Socket.instances = []
  shell = new WebSocketShell(info('early-part'), new ShellRateGroup())
  await shell.reconnect(); socket = Socket.instances[0]; socket.onopen?.()
  socket.onmessage?.({ data: JSON.stringify({ type: 'replay', bytes: 25 }) })
  socket.onmessage?.({ data: bytesOf('EARLY-PREFIX\r\n') })
  out = []
  shell.subscribe((bytes, replay) => out.push([new TextDecoder().decode(bytes), !!replay]), () => {})
  socket.onmessage?.({ data: bytesOf('LATE-TAIL\r\n') })
  socket.onmessage?.({ data: bytesOf('LIVE\r\n') })
  expect(text(out)).toEqual(['R:\x1bc', 'R:EARLY-PREFIX\r\n', 'R:LATE-TAIL\r\n', 'L:LIVE\r\n'])
  shell.disconnect()
})
it('drops a pending server replay when the transport forgets its scrollback', async () => {
  const shell = new WebSocketShell(info('early-forget'), new ShellRateGroup())
  await shell.reconnect(); const socket = Socket.instances[0]; socket.onopen?.()
  socket.onmessage?.({ data: JSON.stringify({ type: 'replay', bytes: 7 }) })
  socket.onmessage?.({ data: bytesOf('PRIVATE') })
  shell.forget()
  const out: Uint8Array[] = []
  shell.subscribe(bytes => out.push(bytes), () => {})
  expect(out).toEqual([])
  shell.disconnect()
})
it('accepts one replay notice per socket, only before live output, and again on a new socket', async () => {
  const notice = (bytes: number) => ({ data: JSON.stringify({ type: 'replay', bytes }) })
  for (const frames of [
    // A second notice after the first replay finished.
    [notice(3), bytesOf('abc'), notice(3)],
    // A second notice after an empty replay.
    [notice(0), notice(3)],
    // A first notice after live output.
    [bytesOf('live'), notice(3)],
  ]) {
    Socket.instances = []
    const shell = new WebSocketShell(info('once'), new ShellRateGroup())
    shell.subscribe(() => {}, () => {})
    await shell.reconnect(); const socket = Socket.instances[0]; socket.onopen?.()
    for (const frame of frames) socket.onmessage?.(typeof frame === 'object' && 'data' in frame ? frame : { data: frame })
    expect(socket.closed).toBe(true)
  }
  Socket.instances = []
  const shell = new WebSocketShell(info('again'), new ShellRateGroup()); const out: string[] = []
  shell.subscribe((bytes, replay) => { if (replay) out.push(new TextDecoder().decode(bytes)) }, () => {})
  await shell.reconnect(); Socket.instances[0].onopen?.()
  Socket.instances[0].onmessage?.(notice(0))
  await shell.reconnect(); Socket.instances[1].onopen?.()
  Socket.instances[1].onmessage?.(notice(2))
  Socket.instances[1].onmessage?.({ data: bytesOf('ok') })
  expect(Socket.instances[1].closed).toBe(false)
  expect(out).toEqual(['\x1bc', '\x1bc', 'ok'])
  shell.disconnect()
})
it.each(['inventory', 'eviction'])('never regrows forgotten scrollback from a replay still arriving (%s cleanup)', async cleanup => {
  const { apiFetch } = await import('./apiFetch')
  const { restoreShells, forgetRetainedShells } = await import('./shellTransport')
  forgetRetainedShells('desktop') // start from no kept shells, so ours is the oldest
  Socket.instances = []
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify([{ ...info('forgotten'), attached: false }])))
  const [shell] = await restoreShells('forget-session', new ShellRateGroup())
  const socket = Socket.instances[0]; socket.onopen?.()
  socket.onmessage?.({ data: JSON.stringify({ type: 'replay', bytes: 6 }) })
  socket.onmessage?.({ data: bytesOf('abc') })
  if (cleanup === 'inventory') {
    // The session no longer lists the shell: its kept scrollback is erased.
    vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify([])))
    await restoreShells('forget-session', new ShellRateGroup())
  } else {
    // Sixty-four newer kept shells evict the oldest.
    for (let n = 0; n < 64; n++) {
      vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify([{ ...info(`newer-${n}`), attached: true }])))
      await restoreShells(`newer-session-${n}`, new ShellRateGroup())
    }
  }
  socket.onmessage?.({ data: bytesOf('def') }) // the replay's tail, after the erase
  const out: [string, boolean][] = []
  shell.subscribe((bytes, replay) => out.push([new TextDecoder().decode(bytes), !!replay]), () => {})
  expect(out).toEqual([])
  socket.onmessage?.({ data: bytesOf('LIVE') })
  expect(out).toEqual([['LIVE', false]])
  expect(socket.closed).toBe(false)
  socket.onmessage?.({ data: JSON.stringify({ type: 'replay', bytes: 1 }) }) // the guard still holds
  expect(socket.closed).toBe(true)
  forgetRetainedShells('desktop')
})
it('closes a socket that sends an empty replay frame, subscribed or not', async () => {
  for (const subscribed of [false, true]) {
    Socket.instances = []
    const shell = new WebSocketShell(info('empty-frame'), new ShellRateGroup())
    if (subscribed) shell.subscribe(() => {}, () => {})
    await shell.reconnect(); const socket = Socket.instances[0]; socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({ type: 'replay', bytes: 1 }) })
    socket.onmessage?.({ data: new Uint8Array(0).buffer })
    expect(socket.closed).toBe(true)
  }
})

it('gives a shell an inactivity limit ended no replay: a later view starts blank', async () => {
  Socket.instances = []
  const shell = new WebSocketShell(info('ended-no-replay'), new ShellRateGroup())
  const first = vi.fn()
  const unsubscribe = shell.subscribe(first, () => {}); await shell.reconnect(); const socket = Socket.instances[0]; socket.onopen?.()
  socket.onmessage?.({ data: text('ENDED-PRIVATE') })
  expect(first).toHaveBeenCalled()
  socket.onmessage?.({ data: JSON.stringify({ type: 'ended', reason: 'detached', after_seconds: 300 }) })
  unsubscribe()
  const later = vi.fn()
  shell.subscribe(later, () => {})()
  expect(later).not.toHaveBeenCalled()
  expect(shell.endedMessage()).toBe('Shell ended after 5 minutes with no browser attached.')
})

it('a dropped shell reconnects by itself with a bounded backoff, then gives up as Disconnected', async () => {
  realTimeRecovery()
  const { apiFetch } = await import('./apiFetch')
  vi.mocked(apiFetch).mockReset()
  vi.mocked(apiFetch).mockImplementation(async () => new Response('{}', { status: 503 }))
  const shell = new WebSocketShell(info('flaky'), new ShellRateGroup(), undefined, 'session')
  const states: string[] = []
  shell.subscribe(() => {}, state => states.push(state))
  await shell.reconnect(); Socket.instances[0].onopen?.()
  Socket.instances[0].onclose?.()
  expect(states.at(-1)).toBe('connecting')
  expect(shell.reconnecting()).toBe(true)
  await vi.advanceTimersByTimeAsync(400)
  expect(apiFetch).not.toHaveBeenCalled()
  await vi.advanceTimersByTimeAsync(200)
  expect(apiFetch).toHaveBeenCalledTimes(1)
  // Tries at 0.5, 1.5, 3.5, 7.5, 15.5, 25.5, 35.5, 45.5 and 55.5 s; another wait would pass the minute.
  await vi.advanceTimersByTimeAsync(54000)
  expect(states.at(-1)).toBe('connecting')
  await vi.advanceTimersByTimeAsync(2000)
  expect(states.at(-1)).toBe('disconnected')
  expect(shell.reconnecting()).toBe(false)
  const asked = vi.mocked(apiFetch).mock.calls.length
  expect(asked).toBeLessThanOrEqual(10)
  await vi.advanceTimersByTimeAsync(60000)
  expect(vi.mocked(apiFetch).mock.calls.length).toBe(asked)
  // No socket was opened while the server could not say the shell is free.
  expect(Socket.instances).toHaveLength(1)
})

it('an explicit disconnect stops a reconnect in progress', async () => {
  const { apiFetch } = await import('./apiFetch')
  vi.mocked(apiFetch).mockReset()
  vi.mocked(apiFetch).mockImplementation(async () => new Response(JSON.stringify([{ ...info('left'), attached: false }])))
  const shell = new WebSocketShell(info('left'), new ShellRateGroup(), undefined, 'session')
  shell.subscribe(() => {}, () => {})
  await shell.reconnect(); Socket.instances[0].onopen?.()
  Socket.instances[0].onclose?.()
  shell.disconnect()
  await vi.advanceTimersByTimeAsync(5000)
  expect(apiFetch).not.toHaveBeenCalled()
  expect(Socket.instances).toHaveLength(1)
})

// Review P2 1: every try has deadlines, and the minute is real time, so nothing that hangs keeps
// "Reconnecting…" up for ever.
const realTimeRecovery = () => { vi.useRealTimers(); vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] }) }
it('gives up within the minute when the inventory never answers, aborting each request', async () => {
  realTimeRecovery()
  const { apiFetch } = await import('./apiFetch')
  vi.mocked(apiFetch).mockReset()
  const signals: AbortSignal[] = []
  vi.mocked(apiFetch).mockImplementation((_url, init) => { signals.push(init!.signal!); return new Promise<Response>(() => {}) })
  const shell = new WebSocketShell(info('hang'), new ShellRateGroup(), undefined, 'session')
  const states: string[] = []
  shell.subscribe(() => {}, state => states.push(state))
  await shell.reconnect(); Socket.instances[0].onopen?.()
  Socket.instances[0].onclose?.()
  await vi.advanceTimersByTimeAsync(61000)
  expect(states.at(-1)).toBe('disconnected')
  expect(shell.reconnecting()).toBe(false)
  expect(shell.disconnectedReason()).toBe('outage')
  expect(signals.length).toBeGreaterThan(0)
  expect(signals.every(signal => signal.aborted)).toBe(true)
})
it('abandons a socket that never opens and gives up within the minute', async () => {
  realTimeRecovery()
  const { apiFetch } = await import('./apiFetch')
  vi.mocked(apiFetch).mockReset()
  vi.mocked(apiFetch).mockImplementation(async () => new Response(JSON.stringify([{ ...info('silent'), attached: false }])))
  const shell = new WebSocketShell(info('silent'), new ShellRateGroup(), undefined, 'session')
  const states: string[] = []
  shell.subscribe(() => {}, state => states.push(state))
  await shell.reconnect(); Socket.instances[0].onopen?.()
  Socket.instances[0].onclose?.()
  await vi.advanceTimersByTimeAsync(20000)
  // The first try's socket never opened: it was abandoned at its deadline and another try followed.
  expect(Socket.instances.length).toBeGreaterThan(2)
  expect(Socket.instances.slice(0, -1).every(socket => socket.closed)).toBe(true)
  await vi.advanceTimersByTimeAsync(45000)
  expect(states.at(-1)).toBe('disconnected')
  expect(shell.disconnectedReason()).toBe('outage')
})
// Review P2 4: only an inventory that confirms another tab holds the shell says so.
it('reports a confirmed other-tab attachment as elsewhere, not as an outage', async () => {
  const { apiFetch } = await import('./apiFetch')
  vi.mocked(apiFetch).mockReset()
  vi.mocked(apiFetch).mockImplementation(async () => new Response(JSON.stringify([{ ...info('taken'), attached: true }])))
  const shell = new WebSocketShell(info('taken'), new ShellRateGroup(), undefined, 'session')
  shell.subscribe(() => {}, () => {})
  await shell.reconnect(); Socket.instances[0].onopen?.()
  Socket.instances[0].onclose?.()
  await vi.advanceTimersByTimeAsync(1000)
  expect(shell.disconnectedReason()).toBe('elsewhere')
  expect(Socket.instances).toHaveLength(1)
  await shell.reconnect()
  expect(shell.disconnectedReason()).toBeUndefined()
  shell.disconnect()
})

// Re-review P2 1: the held keys sent when the shell runs can disconnect it (a full input queue).
// That disconnect is this page's: no heartbeat of the old socket may later restart a reconnect.
it('a disconnect while publishing running installs no heartbeat and never restarts a reconnect', async () => {
  // The heartbeat is an interval: fake it too.
  vi.useRealTimers(); vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
  const { apiFetch } = await import('./apiFetch')
  vi.mocked(apiFetch).mockReset()
  vi.mocked(apiFetch).mockImplementation(async () => new Response(JSON.stringify([{ ...info('overflow'), attached: false }])))
  const shell = new WebSocketShell(info('overflow'), new ShellRateGroup(), undefined, 'session')
  const states: string[] = []
  shell.subscribe(() => {}, state => { states.push(state); if (state === 'running') shell.disconnect() })
  await shell.reconnect(); Socket.instances[0].onopen?.()
  expect(states.at(-1)).toBe('disconnected')
  await vi.advanceTimersByTimeAsync(40000)
  expect(apiFetch).not.toHaveBeenCalled()
  expect(Socket.instances).toHaveLength(1)
  expect(Socket.instances[0].frames).toEqual([])
  expect(states.at(-1)).toBe('disconnected')
  expect(shell.reconnecting()).toBe(false)
})
