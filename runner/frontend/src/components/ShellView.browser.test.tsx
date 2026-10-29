import '../index.css'
import './ShellWorkspace.css'
import { act, render, waitFor } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import ShellView from './ShellView'
import { FakeShell } from '../test/shellMock'
import { restoreShells, ShellRateGroup } from '../shellTransport'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../authClient', () => ({ ensureFreshToken: async () => true, getAccessToken: () => 'fixture-token' }))

// The app sets letter-spacing on the root. Inherited into xterm's rows, it pushed each glyph off the
// cell grid, so near the end of a line the mouse mapped one column short and selection and copy
// lost the last characters ("/…/core" copied as "/…/cor").
it('keeps terminal text on the cell grid so selection reaches the end of a line', async () => {
  const shell = new FakeShell('grid')
  const view = render(<div className="shell-terminal" style={{ width: 900, height: 300, display: 'flex' }}><ShellView transport={shell} /></div>)
  await waitFor(() => expect(shell.resizes.length).toBeGreaterThan(0))
  const line = 'e'.repeat(60)
  await act(async () => { shell.emit(`${line}\r\n`) })
  const row = await waitFor(() => {
    const found = [...view.container.querySelectorAll('.xterm-rows > div')].find(r => r.textContent?.startsWith(line))
    expect(found).toBeTruthy(); return found as HTMLElement
  })
  const cols = shell.resizes.at(-1)![0]
  const cell = view.container.querySelector('.xterm-screen')!.getBoundingClientRect().width / cols
  // Width of exactly the 60 characters, wherever xterm split them into spans.
  const walker = document.createTreeWalker(row, NodeFilter.SHOW_TEXT)
  let drawn = 0, left = 60
  for (let node = walker.nextNode(); node && left > 0; node = walker.nextNode()) {
    const take = Math.min(left, node.textContent!.length), range = document.createRange()
    range.setStart(node, 0); range.setEnd(node, take); drawn += range.getBoundingClientRect().width; left -= take
  }
  expect(getComputedStyle(row).letterSpacing).toMatch(/^(normal|0px)$/)
  expect(Math.abs(drawn - 60 * cell)).toBeLessThan(2)
  view.unmount()
})

// #257 review: returning to a session replays what the person saw. A terminal query in
// that history (cursor position, ESC[6n) was answered when it first ran; answering it
// again would type the reply into whatever runs in the shell now.
it('never answers terminal queries in replayed scrollback, but answers a new live query', async () => {
  const shell = new FakeShell('replay')
  const reply = new RegExp(`${String.fromCharCode(27)}\\[\\d+;\\d+R`)
  shell.replay = ['before \u001b[6n', '\u001b[c\u001b[5n replayed-marker\r\n']
  const view = render(<div className="shell-terminal" style={{ width: 900, height: 300, display: 'flex' }}><ShellView transport={shell} /></div>)
  await waitFor(() => expect(view.container.querySelector('.xterm-rows')?.textContent).toContain('replayed-marker'))
  await new Promise(resolve => setTimeout(resolve, 50))
  const sent = () => shell.inputs.map(bytes => new TextDecoder().decode(bytes))
  expect(sent()).toEqual([])
  await act(async () => { shell.emit('live \u001b[6n') })
  await waitFor(() => expect(sent().some(data => reply.test(data))).toBe(true))
  expect(shell.activities.every(human => !human)).toBe(true)
  view.unmount()
})

// #257 round 2: returning to a session hands the kept scrollback to a new xterm, which
// parses it asynchronously. A confirmed sign-out in that window clears the kept history
// and closes the shell; the write already queued in the terminal must not paint the
// private output back onto the "Shell ended" surface.
it('never repaints queued private replay after sign-out cleared the shell', async () => {
  class Socket {
    static OPEN = 1
    static instances: Socket[] = []
    readyState = 1; bufferedAmount = 0; binaryType = ''
    closed = false
    onopen?: () => void
    onmessage?: (event: { data: ArrayBuffer | string }) => void
    onclose?: () => void
    constructor() { Socket.instances.push(this) }
    send() {}
    close() { this.closed = true }
  }
  vi.stubGlobal('WebSocket', Socket)
  try {
    apiFetch.mockImplementation(async () => new Response(JSON.stringify([{ handle: 'private', shell_id: 'private', runtime: 'host', folder: 'project', attached: false }])))
    const [shell] = await restoreShells('private-session', new ShellRateGroup())
    const box = () => <div className="shell-terminal" style={{ width: 900, height: 300, display: 'flex' }}><ShellView transport={shell} /></div>
    const first = render(box())
    act(() => { Socket.instances[0].onopen?.() })
    act(() => { Socket.instances[0].onmessage?.({ data: new TextEncoder().encode('PRIVATE-PENDING-REPLAY\r\n').buffer }) })
    await waitFor(() => expect(first.container.querySelector('.xterm-rows')?.textContent).toContain('PRIVATE-PENDING-REPLAY'))
    // Leave the session, then come back: the new terminal gets the replay queued.
    first.unmount(); shell.disconnect()
    const second = render(box())
    // Confirmed sign-out before xterm parses that replay.
    window.dispatchEvent(new Event('blerg-signed-out'))
    await new Promise(resolve => setTimeout(resolve, 100))
    await new Promise(requestAnimationFrame); await new Promise(requestAnimationFrame)
    expect(second.container).toHaveTextContent('This shell ended.')
    const surface = second.container.querySelector('.shell-terminal-renderer')!
    expect(surface.textContent).not.toContain('PRIVATE')
    expect(surface.querySelector('.xterm-rows')?.textContent ?? '').not.toContain('PRIVATE')
    expect(second.container.querySelectorAll('.xterm')).toHaveLength(1)
    // The transport kept nothing either.
    const replay: Uint8Array[] = [], states: string[] = []
    shell.subscribe(bytes => replay.push(bytes), state => states.push(state))()
    expect(replay).toEqual([])
    expect(states.at(-1)).toBe('closed')
    second.unmount()
  } finally { vi.unstubAllGlobals() }
})

it('names the inactivity limit that ended the shell and keeps its last screen', async () => {
  class EndedShell extends FakeShell { message = ''; endedMessage() { return this.message } }
  const shell = new EndedShell('ended')
  const view = render(<div className="shell-terminal" style={{ width: 900, height: 300, display: 'flex' }}><ShellView transport={shell} /></div>)
  await waitFor(() => expect(shell.resizes.length).toBeGreaterThan(0))
  await act(async () => { shell.emit('last-output-line\r\n') })
  await waitFor(() => expect(view.container.querySelector('.xterm-rows')?.textContent).toContain('last-output-line'))
  shell.message = 'Shell ended after 30 minutes without typing.'
  await act(async () => { shell.state?.('closed') })
  expect(view.getByRole('status')).toHaveTextContent('Shell ended after 30 minutes without typing.')
  expect(view.container.querySelector('.xterm-rows')?.textContent).toContain('last-output-line')
  view.unmount()
})

// Re-review: the last screen of a limit-ended shell is kept only for the login that owned it.
it.each([
  ['blerg-signed-out', false],
  ['blerg-login-changed', true],
] as const)('erases a limit-ended terminal on %s, even after the session stopped listing it (%s)', async (event, unlisted) => {
  class Socket {
    static OPEN = 1
    static instances: Socket[] = []
    readyState = 1; bufferedAmount = 0; binaryType = ''
    closed = false
    onopen?: () => void
    onmessage?: (event: { data: ArrayBuffer | string }) => void
    onclose?: () => void
    constructor() { Socket.instances.push(this) }
    send() {}
    close() { this.closed = true }
  }
  vi.stubGlobal('WebSocket', Socket)
  try {
    const id = `limit-${event}`
    apiFetch.mockImplementation(async () => new Response(JSON.stringify([{ handle: id, shell_id: id, runtime: 'host', folder: 'project', attached: false }])))
    const [shell] = await restoreShells(`session-${id}`, new ShellRateGroup())
    const view = render(<div className="shell-terminal" style={{ width: 900, height: 300, display: 'flex' }}><ShellView transport={shell} /></div>)
    const socket = Socket.instances.at(-1)!
    act(() => { socket.onopen?.() })
    act(() => { socket.onmessage?.({ data: new TextEncoder().encode('LIMIT-PRIVATE-LINE\r\n').buffer }) })
    const rows = () => view.container.querySelector('.shell-terminal-renderer')?.textContent ?? ''
    await waitFor(() => expect(rows()).toContain('LIMIT-PRIVATE-LINE'))
    act(() => { socket.onmessage?.({ data: JSON.stringify({ type: 'ended', reason: 'idle', after_seconds: 1800 }) }) })
    expect(view.getByRole('status')).toHaveTextContent('Shell ended after 30 minutes without typing.')
    expect(rows()).toContain('LIMIT-PRIVATE-LINE')
    if (unlisted) {
      // The inventory no longer lists the ended shell, so it is no longer kept for replay.
      apiFetch.mockImplementation(async () => new Response('[]'))
      await restoreShells(`session-${id}`, new ShellRateGroup())
    }
    act(() => { window.dispatchEvent(new Event(event)) })
    await new Promise(resolve => setTimeout(resolve, 100))
    await new Promise(requestAnimationFrame); await new Promise(requestAnimationFrame)
    expect(rows()).not.toContain('LIMIT-PRIVATE-LINE')
    expect(view.getByRole('status')).toHaveTextContent('This shell ended.')
    expect(view.getByRole('status')).not.toHaveTextContent('without typing')
    expect(shell.endedMessage?.()).toBe('')
    view.unmount()
  } finally { vi.unstubAllGlobals() }
})
