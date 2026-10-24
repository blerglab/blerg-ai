import { apiFetch } from './apiFetch'
import { ensureFreshToken, getAccessToken } from './authClient'

export type ShellState = 'connecting' | 'syncing' | 'running' | 'disconnected' | 'closed'
export type ShellInfo = { handle: string; shell_id: string; runtime: 'sandbox' | 'cluster' | 'host'; folder: string; attached?: boolean }
// Rendering has no dependency on the agent socket, tmux, History, or session input.
export interface ShellTransport {
  readonly info: ShellInfo
  subscribe(output: (bytes: Uint8Array) => void, state: (state: ShellState) => void): () => void
  input(bytes: Uint8Array, human?: boolean): void
  activity?(): void
  resize(cols: number, rows: number): void
  reconnect(takeover?: boolean): Promise<void>
  disconnect(): void
  end(): Promise<void>
}

// One bounded input scheduler for the whole session, including all splits and pop-out panes.
export class ShellRateGroup {
  private queue: { owner: WebSocketShell; data: Uint8Array; human: boolean }[] = []
  private bytes = 0
  private timer: ReturnType<typeof setTimeout> | undefined
  enqueue(owner: WebSocketShell, data: Uint8Array, human = false) {
    if (this.bytes + data.length > 65536) { owner.disconnect(); return }
    for (let offset = 0; offset < data.length; offset += 4096) {
      const chunk = data.slice(offset, offset + 4096)
      this.queue.push({ owner, data: chunk, human }); this.bytes += chunk.length
    }
    this.schedule()
  }
  remove(owner: WebSocketShell) {
    this.queue = this.queue.filter(item => item.owner !== owner)
    this.bytes = this.queue.reduce((n, item) => n + item.data.length, 0)
    if (!this.queue.length && this.timer) { clearTimeout(this.timer); this.timer = undefined }
  }
  private schedule() {
    if (this.timer || !this.queue.length) return
    this.timer = setTimeout(() => {
      this.timer = undefined
      const item = this.queue.shift()
      if (item) { this.bytes -= item.data.length; item.owner.sendInput(item.data, item.human) }
      this.schedule()
    }, 260) // Below 16 KiB/s, with room for scheduler jitter across second boundaries.
  }
}

function loginIdentity(token: string | null): string {
  if (!token) return ''
  try { const p = JSON.parse(atob(token.split('.')[1].replace(/-/g, '+').replace(/_/g, '/'))) as { sub?: string; sid?: string; kind?: string }; return `${p.kind}:${p.sub}:${p.sid}` }
  catch { return token }
}
export class WebSocketShell implements ShellTransport {
  private socket: WebSocket | undefined
  private output: ((bytes: Uint8Array) => void) | undefined
  private changed: ((state: ShellState) => void) | undefined
  private state: ShellState = 'disconnected'
  private heartbeat: ReturnType<typeof setInterval> | undefined
  private lastActivity = 0
  private inputSeq = 0
  private resizeSeq = 0
  private generation = 0
  private identity = ''
  readonly info: ShellInfo
  private readonly group: ShellRateGroup
  constructor(info: ShellInfo, group: ShellRateGroup) { this.info = info; this.group = group }
  subscribe(output: (bytes: Uint8Array) => void, state: (state: ShellState) => void) {
    this.output = output; this.changed = state; state(this.state)
    return () => { if (this.output === output) { this.output = undefined; this.changed = undefined } }
  }
  private status(state: ShellState) { this.state = state; this.changed?.(state) }
  async reconnect(takeover = false) {
    this.disconnect()
    const generation = ++this.generation
    this.status('connecting')
    await ensureFreshToken()
    if (generation !== this.generation) return
    const token = getAccessToken()
    if (!token) { this.status('disconnected'); return }
    const url = new URL(`/api/shells/${encodeURIComponent(this.info.handle)}/socket`, location.origin)
    url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
    if (takeover) url.searchParams.set('takeover', 'true')
    this.identity = loginIdentity(token)
    const socket = new WebSocket(url, ['bearer', token]); socket.binaryType = 'arraybuffer'
    this.socket = socket; this.inputSeq = 0; this.resizeSeq = 0
    socket.onopen = () => {
      if (generation !== this.generation) { socket.close(); return }
      this.status('running')
      this.heartbeat = setInterval(() => { this.send({ type: 'heartbeat' }) }, 15000)
    }
    socket.onmessage = event => {
      if (generation !== this.generation || !this.sameIdentity()) return
      if (typeof event.data === 'string') {
        try { const frame = JSON.parse(event.data) as {type?: string; phase?: string}; if (frame.type === 'phase' && (frame.phase === 'syncing' || frame.phase === 'running')) { this.status(frame.phase); return } } catch { /* Invalid control closes this dedicated channel. */ }
        this.disconnect(); return
      }
      if (!(event.data instanceof ArrayBuffer) || event.data.byteLength > 16384) { this.disconnect(); return }
      this.output?.(new Uint8Array(event.data))
    }
    socket.onclose = () => { if (generation === this.generation) this.disconnect() }
    socket.onerror = () => { if (generation === this.generation) this.disconnect() }
  }
  private sameIdentity(): boolean {
    if (!this.identity || loginIdentity(getAccessToken()) !== this.identity) { this.disconnect(); this.status('closed'); return false }
    return true
  }
  private send(frame: object) {
    if (!this.sameIdentity()) return
    if (this.socket?.readyState !== WebSocket.OPEN || this.socket.bufferedAmount > 65536) { this.disconnect(); return }
    this.socket.send(JSON.stringify(frame))
  }
  activity() { const now = Date.now(); if (this.state === 'running' && now - this.lastActivity >= 1000) { this.lastActivity = now; this.send({ type: 'activity' }) } }
  input(bytes: Uint8Array, human = false) { if (this.state === 'running') this.group.enqueue(this, bytes, human) }
  sendInput(bytes: Uint8Array, human = false) {
    if (this.state !== 'running') return
    let binary = ''; for (const b of bytes) binary += String.fromCharCode(b)
    this.send({ type: 'input', human, seq: ++this.inputSeq, data: btoa(binary) })
  }
  resize(cols: number, rows: number) {
    if (this.state !== 'running') return
    this.send({ type: 'resize', seq: ++this.resizeSeq, cols: Math.min(65535, Math.max(1, cols)), rows: Math.min(65535, Math.max(1, rows)) })
  }
  disconnect() {
    this.generation++; this.group.remove(this)
    if (this.heartbeat) clearInterval(this.heartbeat)
    this.heartbeat = undefined
    const old = this.socket; this.socket = undefined
    old?.close(); if (this.state !== 'closed') this.status('disconnected')
  }
  async end() {
    const response = await apiFetch(`/api/shells/${encodeURIComponent(this.info.handle)}`, { method: 'DELETE' })
    if (!response.ok) throw new Error('Shell could not be ended.')
    this.disconnect(); this.status('closed')
  }
}
export type HostShellChallenge = { challenge_id: string; machine_id: string; account: string; uid: number; shell: string; cwd: string }
export class HostShellConsentRequired extends Error {
  readonly challenge: HostShellChallenge
  constructor(challenge: HostShellChallenge) { super('Host shell requires your consent.'); this.challenge = challenge }
}
export async function acceptHostShell(session: string, challenge: string) {
  const response = await apiFetch(`/api/sessions/${encodeURIComponent(session)}/shell-consent/accept`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ challenge_id: challenge, accept: true }) })
  if (!response.ok) throw new Error('Consent expired or the machine, account, or launch changed. Open the shell again.')
}
export async function openShell(session: string, group: ShellRateGroup): Promise<ShellTransport> {
  const response = await apiFetch(`/api/sessions/${encodeURIComponent(session)}/shells`, { method: 'POST' })
  if (response.status === 428) {
    const challenge = await apiFetch(`/api/sessions/${encodeURIComponent(session)}/shell-consent/challenge`, { method: 'POST' })
    if (!challenge.ok) throw new Error('Host shell challenge unavailable.')
    throw new HostShellConsentRequired(await challenge.json() as HostShellChallenge)
  }
  if (!response.ok) throw new Error(response.status === 429 ? 'This session already has four shells.' : 'Shell is unavailable for this session.')
  const shell = new WebSocketShell(await response.json() as ShellInfo, group)
  await shell.reconnect()
  return shell
}

export async function restoreShells(session: string, group: ShellRateGroup, signal?: AbortSignal): Promise<ShellTransport[]> {
  const response = await apiFetch(`/api/sessions/${encodeURIComponent(session)}/shells`, { signal })
  if (!response.ok) return []
  const infos = await response.json() as ShellInfo[]
  if (!Array.isArray(infos) || infos.length > 4) return []
  const transports = infos.map(info => new WebSocketShell(info, group))
  await Promise.all(transports.map(async shell => { try { if (!shell.info.attached) await shell.reconnect() } catch { shell.disconnect() } }))
  return transports
}
