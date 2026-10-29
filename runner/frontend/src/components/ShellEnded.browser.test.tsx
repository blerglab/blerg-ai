// A shell that is gone (its authorization expired or was revoked, the server no longer lists it, or
// it ended) says so in its own tab and offers New shell, which takes the same tab position. An
// unexpected drop reconnects by itself; another tab's control never does (0.5.0, owner test).
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import ShellWorkspace from '../test/ShellWorkspaceHarness'
import CommandPaletteProvider from './CommandPalette'
import { disposeKeptTerminals } from '../lib/keptTerminals'
import { forgetRetainedShells } from '../shellTransport'
import ShellView from './ShellView'
import { FakeShell } from '../test/shellMock'
import '../index.css'

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), ensureFreshToken: async () => true, getAccessToken: () => 'fixture-token' }))
vi.mock('../ws', () => import('../test/wsMock'))

class LiveSocket {
  static OPEN = 1
  static instances: LiveSocket[] = []
  /** Handles whose sockets the server refuses before the upgrade (the browser sees only a close). */
  static refused = new Set<string>()
  readyState = 1; bufferedAmount = 0; binaryType = ''
  closed = false
  sent: string[] = []
  url: string
  onopen?: () => void
  onmessage?: (event: { data: ArrayBuffer | string }) => void
  onerror?: () => void
  onclose?: () => void
  constructor(url: URL | string) {
    this.url = String(url); LiveSocket.instances.push(this)
    const handle = /\/api\/shells\/([^/]+)\/socket/.exec(this.url)?.[1] ?? ''
    queueMicrotask(() => { if (LiveSocket.refused.has(handle)) { this.closed = true; this.onerror?.(); this.onclose?.() } else this.onopen?.() })
  }
  send(frame: string) { this.sent.push(frame) }
  close() { this.closed = true }
  /** The server ends the connection (a restart, a takeover): the browser sees a close. */
  drop() { this.closed = true; this.onclose?.() }
}
const socketsFor = (handle: string) => LiveSocket.instances.filter(socket => socket.url.includes(`/api/shells/${handle}/socket`))

type Listed = { handle: string; shell_id: string; runtime: 'host'; folder: string; attached?: boolean }
const shell = (id: string): Listed => ({ handle: `h-${id}`, shell_id: `shell-${id}`, runtime: 'host', folder: 'project' })
/** The fake server: the shells it lists, whether the inventory fails, and the shells it creates. */
const server = { listed: [] as Listed[], failInventory: false, creates: 0, next: 'new', attachedElsewhere: new Set<string>(), holdCreate: null as Promise<void> | null, deleted: [] as string[], failDelete: false }
beforeEach(() => {
  LiveSocket.instances = []; LiveSocket.refused = new Set()
  vi.stubGlobal('WebSocket', LiveSocket)
  server.listed = []; server.failInventory = false; server.creates = 0; server.next = 'new'; server.attachedElsewhere = new Set(); server.holdCreate = null; server.deleted = []; server.failDelete = false
  apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.endsWith('/shell-capability')) return new Response(JSON.stringify({ available: true }))
    if (/\/api\/sessions\/[^/]+\/shells$/.test(url)) {
      if (init?.method === 'POST') {
        server.creates++
        if (server.holdCreate) await server.holdCreate
        const created = shell(server.next)
        server.listed = [...server.listed, created]
        return new Response(JSON.stringify(created), { status: 201 })
      }
      if (server.failInventory) return new Response('{"error":"shell unavailable"}', { status: 403 })
      return new Response(JSON.stringify(server.listed.map(s => ({ ...s, attached: server.attachedElsewhere.has(s.handle) || socketsFor(s.handle).some(socket => !socket.closed) }))))
    }
    if (init?.method === 'DELETE') {
      const handle = decodeURIComponent(url.split('/').at(-1)!)
      if (server.failDelete) return new Response('{"error":"shell unavailable"}', { status: 503 })
      server.deleted.push(handle); server.listed = server.listed.filter(s => s.handle !== handle)
      return new Response(null, { status: 204 })
    }
    return new Response('{}', { status: 404 })
  })
})
afterEach(() => { forgetRetainedShells('login'); disposeKeptTerminals(); localStorage.clear(); vi.unstubAllGlobals() })

const view = (session: string) => <MemoryRouter initialEntries={[`/sessions/${session}`]}><CommandPaletteProvider><div style={{ height: '100dvh' }}>
  <ShellWorkspace sessionId={session} accountId="owner"><div className="agent-composer"><textarea aria-label="Composer" /></div></ShellWorkspace>
</div></CommandPaletteProvider></MemoryRouter>
const screenText = () => document.querySelector('.xterm-screen')?.textContent ?? ''
const say = (socket: LiveSocket, text: string) => act(() => { socket.onmessage?.({ data: new TextEncoder().encode(text).buffer }) })
const shellTabIds = () => [...document.querySelectorAll<HTMLElement>('[data-tab-id^="shell:"]')].map(tab => tab.dataset.tabId)

/** A session whose shells were open on this page, then the page was refreshed (nothing kept but the tabs). */
async function refreshed(session: string, ids: string[]) {
  server.listed = ids.map(shell)
  const first = render(view(session))
  await waitFor(() => expect(shellTabIds()).toEqual(ids.map(id => `shell:shell-${id}`)))
  await waitFor(() => expect(LiveSocket.instances.length).toBe(ids.length))
  first.unmount()
  disposeKeptTerminals(); forgetRetainedShells('login')
  LiveSocket.instances.forEach(socket => { socket.closed = true })
}

it('a restored shell whose socket is refused and that the server no longer lists says it ended and offers New shell, never a blank terminal', async () => {
  await refreshed('revoked', ['a'])
  // The server still lists the shell when the page asks, but its authorization is gone: the socket is
  // refused, and by the time the browser looks again the server has closed it.
  LiveSocket.refused.add('h-a')
  const sockets = LiveSocket.instances.length
  render(view('revoked'))
  await waitFor(() => expect(LiveSocket.instances.length).toBe(sockets + 1))
  server.listed = []
  fireEvent.click(await screen.findByRole('tab', { name: 'Shell' }))
  const status = await screen.findByText('This shell ended.', undefined, { timeout: 3000 })
  const bar = status.closest('.shell-connection') as HTMLElement
  expect(within(bar).getByRole('button', { name: 'New shell' })).toBeTruthy()
  expect(screen.queryByText(/^(Disconnected\.|Controlled in another tab\.)$/)).toBeNull()
  expect(shellTabIds()).toEqual(['shell:shell-a'])
})

it('a restored tab whose shell the server no longer lists says it ended instead of vanishing', async () => {
  await refreshed('unlisted', ['a'])
  server.listed = []
  render(view('unlisted'))
  await waitFor(() => expect(document.querySelector('[data-shells-restored="true"]')).toBeTruthy())
  fireEvent.click(await screen.findByRole('tab', { name: 'Shell' }))
  const bar = (await screen.findByText('This shell ended.')).closest('.shell-connection') as HTMLElement
  expect(bar).toHaveTextContent('It is no longer running, for example after Blerg was updated.')
  expect(within(bar).getByRole('button', { name: 'New shell' })).toBeTruthy()
})

it('a restored tab whose inventory is refused is never a blank surface', async () => {
  await refreshed('unreachable', ['a'])
  server.failInventory = true
  render(view('unreachable'))
  await waitFor(() => expect(document.querySelector('[data-shells-restored="true"]')).toBeTruthy())
  fireEvent.click(await screen.findByRole('tab', { name: 'Shell' }))
  const bar = (await screen.findByText('This shell could not be reached.')).closest('.shell-connection') as HTMLElement
  expect(within(bar).getByRole('button', { name: 'New shell' })).toBeTruthy()
  expect(document.querySelector('.shell-gone')).toBeTruthy()
})

it('a lease refusal on a live shell says its authorization expired and offers New shell', async () => {
  server.listed = [shell('a')]
  render(view('lease'))
  fireEvent.click(await screen.findByRole('tab', { name: 'Shell' }))
  await waitFor(() => expect(socketsFor('h-a')).toHaveLength(1))
  await say(socketsFor('h-a')[0], 'project $ echo before\r\n')
  await waitFor(() => expect(screenText()).toContain('echo before'))
  act(() => { socketsFor('h-a')[0].onmessage?.({ data: JSON.stringify({ type: 'error', cause: 'lease_check' }) }) })
  const status = await screen.findByText('This shell ended.')
  const bar = status.closest('.shell-connection') as HTMLElement
  expect(bar).toHaveTextContent('Its authorization expired, for example after Blerg was updated.')
  expect(within(bar).getByRole('button', { name: 'New shell' })).toBeTruthy()
  // Its last screen stays.
  expect(screenText()).toContain('echo before')
})

it('New shell starts one shell and puts it in the dead shell’s tab position', async () => {
  await refreshed('replace', ['a', 'b'])
  server.listed = [shell('b')]
  render(view('replace'))
  await waitFor(() => expect(document.querySelector('[data-shells-restored="true"]')).toBeTruthy())
  fireEvent.click(await screen.findByRole('tab', { name: 'Shell' }))
  const bar = (await screen.findByText('This shell ended.')).closest('.shell-connection') as HTMLElement
  fireEvent.click(within(bar).getByRole('button', { name: 'New shell' }))
  await waitFor(() => expect(shellTabIds()).toEqual(['shell:shell-new', 'shell:shell-b']))
  expect(server.creates).toBe(1)
  // The new shell keeps the tab's name and is the selected tab, connected.
  expect(screen.getByRole('tab', { name: 'Shell' }).getAttribute('data-tab-id')).toBe('shell:shell-new')
  expect(screen.getByRole('tab', { name: 'Shell' }).getAttribute('aria-selected')).toBe('true')
  await waitFor(() => expect(socketsFor('h-new')).toHaveLength(1))
  await waitFor(() => expect(screen.queryByText('This shell ended.')).toBeNull())
  expect(server.creates).toBe(1)
})

it('an unexpected drop reconnects by itself, keeping the picture, with no bar and no click', async () => {
  server.listed = [shell('a')]
  render(view('drop'))
  fireEvent.click(await screen.findByRole('tab', { name: 'Shell' }))
  await waitFor(() => expect(socketsFor('h-a')).toHaveLength(1))
  await say(socketsFor('h-a')[0], 'project $ echo kept-picture\r\n')
  await waitFor(() => expect(screenText()).toContain('echo kept-picture'))
  const bars: string[] = []
  const watch = new MutationObserver(() => { const bar = document.querySelector('.shell-connection'); if (bar) bars.push(bar.textContent ?? '') })
  watch.observe(document.body, { childList: true, subtree: true, characterData: true })
  try {
    act(() => socketsFor('h-a')[0].drop())
    await waitFor(() => expect(socketsFor('h-a')).toHaveLength(2), { timeout: 2000 })
    await waitFor(() => expect(socketsFor('h-a')[1].closed).toBe(false))
    await new Promise(r => setTimeout(r, 50))
  } finally { watch.disconnect() }
  expect(socketsFor('h-a')[1].url).not.toContain('takeover')
  expect(bars).toEqual([])
  expect(screenText()).toContain('echo kept-picture')
  expect(screen.queryByText('This shell ended.')).toBeNull()
})

it('a drop because another tab took the shell keeps the bar with Take over and never reconnects by itself', async () => {
  server.listed = [shell('a')]
  render(view('takeover'))
  fireEvent.click(await screen.findByRole('tab', { name: 'Shell' }))
  await waitFor(() => expect(socketsFor('h-a')).toHaveLength(1))
  await say(socketsFor('h-a')[0], 'project $ echo mine\r\n')
  server.attachedElsewhere.add('h-a')
  act(() => socketsFor('h-a')[0].drop())
  expect(await screen.findByText('Controlled in another tab.', undefined, { timeout: 3000 })).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Take over' })).toBeTruthy()
  await new Promise(r => setTimeout(r, 2500))
  expect(socketsFor('h-a')).toHaveLength(1)
  expect(screen.queryByText('This shell ended.')).toBeNull()
})

// Review P2 2: the person closed the dead tab while its replacement was starting.
it('a replacement whose tab was closed while it started is ended, with no tab and no connection', async () => {
  await refreshed('closed-meanwhile', ['a'])
  server.listed = []
  render(view('closed-meanwhile'))
  fireEvent.click(await screen.findByRole('tab', { name: 'Shell' }))
  let release!: () => void
  server.holdCreate = new Promise<void>(resolve => { release = resolve })
  const bar = (await screen.findByText('This shell ended.')).closest('.shell-connection') as HTMLElement
  fireEvent.click(within(bar).getByRole('button', { name: 'New shell' }))
  await waitFor(() => expect(server.creates).toBe(1))
  fireEvent.click(screen.getByRole('button', { name: 'Close Shell' }))
  expect(shellTabIds()).toEqual([])
  await act(async () => { release() })
  await waitFor(() => expect(server.deleted).toContain('h-new'))
  await new Promise(r => setTimeout(r, 50))
  expect(shellTabIds()).toEqual([])
  expect(socketsFor('h-new').every(socket => socket.closed)).toBe(true)
})

// Review P2 3: an old shell that was only unreachable, not gone, must not come back beside its replacement.
it('a replaced shell that a later inventory still lists does not get its tab back, and is ended', async () => {
  await refreshed('replaced-live', ['a'])
  server.failInventory = true
  const ui = render(view('replaced-live'))
  fireEvent.click(await screen.findByRole('tab', { name: 'Shell' }))
  const bar = (await screen.findByText('This shell could not be reached.')).closest('.shell-connection') as HTMLElement
  server.failInventory = false
  fireEvent.click(within(bar).getByRole('button', { name: 'New shell' }))
  await waitFor(() => expect(shellTabIds()).toEqual(['shell:shell-new']))
  ui.unmount()
  // The server still had the old shell all along: the next inventory lists both.
  expect(server.listed.map(s => s.handle)).toEqual(['h-a', 'h-new'])
  render(view('replaced-live'))
  await waitFor(() => expect(document.querySelector('[data-shells-restored="true"]')).toBeTruthy())
  await waitFor(() => expect(server.deleted).toContain('h-a'))
  expect(shellTabIds()).toEqual(['shell:shell-new'])
})

// Review P2 4: an ordinary outage offers Reconnect only; Take over only for a confirmed other tab.
it.each([['outage', 'Disconnected.', false], ['elsewhere', 'Controlled in another tab.', true], [undefined, 'Disconnected.', false]] as const)('a disconnect for reason %s says %s', async (reason, text, takeover) => {
  const shell = new FakeShell(`reason-${reason}`)
  shell.connectionState = 'disconnected'; shell.reason = reason
  const ui = render(<div className="shell-terminal" style={{ width: 600, height: 300, display: 'flex' }}><ShellView transport={shell} /></div>)
  const bar = await ui.findByRole('status')
  expect(bar).toHaveTextContent(text)
  expect(within(bar).getByRole('button', { name: 'Reconnect' })).toBeTruthy()
  expect(!!within(bar).queryByRole('button', { name: 'Take over' })).toBe(takeover)
  ui.unmount()
})

// Re-review P2 2: the person closed the dead tab, then left the session, before the replacement started.
it('a replacement whose tab was closed is ended even when the view left before it started', async () => {
  await refreshed('closed-then-left', ['a'])
  server.listed = []
  const ui = render(view('closed-then-left'))
  fireEvent.click(await screen.findByRole('tab', { name: 'Shell' }))
  let release!: () => void
  server.holdCreate = new Promise<void>(resolve => { release = resolve })
  const bar = (await screen.findByText('This shell ended.')).closest('.shell-connection') as HTMLElement
  fireEvent.click(within(bar).getByRole('button', { name: 'New shell' }))
  await waitFor(() => expect(server.creates).toBe(1))
  fireEvent.click(screen.getByRole('button', { name: 'Close Shell' }))
  ui.unmount()
  await act(async () => { release() })
  await waitFor(() => expect(server.deleted).toContain('h-new'))
  expect(socketsFor('h-new').every(socket => socket.closed)).toBe(true)
  // A return does not bring it back.
  render(view('closed-then-left'))
  await waitFor(() => expect(document.querySelector('[data-shells-restored="true"]')).toBeTruthy())
  expect(shellTabIds()).toEqual([])
  expect(socketsFor('h-new').every(socket => socket.closed)).toBe(true)
})

// Re-review P2: the DELETE of a cancelled replacement fails; a return still must not bring it back.
it('a cancelled replacement whose DELETE fails never comes back, and the next inventory ends it', async () => {
  await refreshed('delete-fails', ['a'])
  server.listed = []
  const ui = render(view('delete-fails'))
  fireEvent.click(await screen.findByRole('tab', { name: 'Shell' }))
  let release!: () => void
  server.holdCreate = new Promise<void>(resolve => { release = resolve })
  const bar = (await screen.findByText('This shell ended.')).closest('.shell-connection') as HTMLElement
  fireEvent.click(within(bar).getByRole('button', { name: 'New shell' }))
  await waitFor(() => expect(server.creates).toBe(1))
  fireEvent.click(screen.getByRole('button', { name: 'Close Shell' }))
  ui.unmount()
  server.failDelete = true
  await act(async () => { release() })
  await new Promise(r => setTimeout(r, 50))
  expect(server.listed.map(s => s.handle)).toEqual(['h-new'])
  server.failDelete = false
  render(view('delete-fails'))
  await waitFor(() => expect(document.querySelector('[data-shells-restored="true"]')).toBeTruthy())
  await waitFor(() => expect(server.deleted).toContain('h-new'))
  expect(shellTabIds()).toEqual([])
  expect(socketsFor('h-new').every(socket => socket.closed)).toBe(true)
})
