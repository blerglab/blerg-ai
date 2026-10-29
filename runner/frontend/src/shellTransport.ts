import { apiFetch } from './apiFetch'
import { ensureFreshToken, getAccessToken } from './authClient'

const causes: Record<string, string> = {
  bootstrap_read: 'The shell helper could not read its launch request.',
  bootstrap_validate: 'The shell helper rejected its launch request.',
  resolve_root: 'The shell project directory could not be resolved.',
  resolve_identity: 'The shell account could not be resolved.',
  verify_identity: 'The shell account changed. Review host shell consent again.',
  verify_cwd: 'The shell working directory changed.',
  verify_root: 'The shell project directory changed.',
  verify_grant: 'Shell authorization could not be verified. Reconnect the desktop.',
  open_root: 'The shell project directory could not be opened.',
  open_cwd: 'The shell working directory could not be opened.',
  verify_uid: 'The shell account ID is unsupported.',
  pty_open: "The shell terminal could not be opened. Check the service's terminal access.",
  exec_shell: 'The account shell could not be executed. Check its permissions and service restrictions.',
  pty_configure: 'The shell terminal could not be configured.',
  containment_setup: "Shell containment could not be established. Check the service's tracing and seccomp restrictions.",
  lease_create: 'The shell authorization expired during startup. Try opening the shell again.',
  helper_ready: 'The shell helper could not confirm startup.',
  helper_stopped: 'The shell helper stopped.',
  helper_input_pipe: "The shell helper's input channel could not be opened.",
  helper_output_pipe: "The shell helper's output channel could not be opened.",
  helper_record: "The shell helper's ownership could not be recorded.",
  helper_write: "The shell helper's request could not be sent.",
  helper_wait: 'The shell helper did not acknowledge the request before it was canceled or timed out.',
  helper_reply: 'The shell helper rejected a control request.',
  admit_shell: 'The shell launch is paused or awaiting recovery.',
  verify_binding: 'The shell launch changed. Reconnect the desktop and review consent again.',
  verify_reservation: 'The shell launch reservation is no longer valid.',
  verify_policy: 'Shell authorization changed. Reconnect the desktop.',
  grant_clock: "This desktop's clock is off by more than a few seconds. Sync it (on WSL: sudo hwclock -s), then try again.",
  cleanup_shell: 'Shell cleanup could not be verified. Check the desktop daemon log.',
  control_read: "The shell helper's control channel closed.",
  control_write: 'The shell helper could not acknowledge a control request.',
  control_grant: 'The shell helper rejected control authorization.',
  lease_check: 'The shell authorization expired or was revoked.',
  input_sequence: 'The shell helper rejected the input sequence.',
  resize_sequence: 'The shell helper rejected the terminal resize sequence.',
  control_operation: 'The shell helper rejected a control operation.',
  control_apply: 'The shell helper could not apply a control operation.',
  workspace_admission: 'The session workspace could not authorize this shell launch. Reconnect the desktop.',
  helper_start: 'The shell helper could not be launched. Check the service process permissions and restrictions.',
  helper_missing: 'A shell executable or its working directory is missing.',
  permission_denied: 'Permission denied starting the shell.',
  start_timeout: 'Shell startup timed out.',
  bootstrap_refused: 'Shell containment or launch verification failed.',
  start_unavailable: 'The shell could not start.',
}
function shellFailureMessage(cause: unknown): string | undefined {
  return typeof cause === 'string' && Object.hasOwn(causes, cause) ? causes[cause] : undefined
}

function plural(n: number, unit: string): string { return `${n} ${unit}${n === 1 ? '' : 's'}` }
export function shellLimitText(seconds: number): string {
  const minutes = Math.round(seconds / 60), hours = Math.floor(minutes / 60), rest = minutes % 60
  if (hours === 0) return plural(minutes, 'minute')
  return rest ? `${plural(hours, 'hour')} ${plural(rest, 'minute')}` : plural(hours, 'hour')
}
// The server names the inactivity limit that ended a shell; nothing else ends one with a cause.
export function shellEndedMessage(reason: unknown, afterSeconds: unknown): string | undefined {
  const after = typeof afterSeconds === 'number' && Number.isFinite(afterSeconds) && afterSeconds > 0 ? ` after ${shellLimitText(afterSeconds)}` : ''
  if (reason === 'idle') return `Shell ended${after} without typing.`
  if (reason === 'detached') return `Shell ended${after} with no browser attached.`
  return undefined
}

export type ShellState = 'connecting' | 'syncing' | 'running' | 'disconnected' | 'closed'
export type ShellInfo = { handle: string; shell_id: string; runtime: 'sandbox' | 'cluster' | 'host'; folder: string; attached?: boolean }
// Rendering has no dependency on the agent socket, tmux, History, or session input.
export interface ShellTransport {
  readonly info: ShellInfo
  /** replay marks output an earlier view already displayed: it must never produce terminal replies. */
  subscribe(output: (bytes: Uint8Array, replay?: boolean) => void, state: (state: ShellState) => void, error?: (message: string) => void): () => void
  input(bytes: Uint8Array, human?: boolean): void
  activity?(): void
  resize(cols: number, rows: number): void
  reconnect(takeover?: boolean): Promise<void>
  disconnect(): void
  end(): Promise<void>
  // Why the shell ended (an inactivity limit, or its authorization is gone), once one has.
  endedMessage?(): string
  /** Whether a dropped connection is being re-established by itself (no click). */
  reconnecting?(): boolean
  /**
   * Why the shell is disconnected, when known: another tab controls it ('elsewhere', confirmed by
   * the session's inventory), or reconnecting by itself gave up ('outage').
   */
  disconnectedReason?(): 'elsewhere' | 'outage' | undefined
}

/** The fixed sentences for a shell that is gone. */
export const SHELL_ENDED = 'This shell ended.'
export const SHELL_AUTHORIZATION_EXPIRED = 'Its authorization expired, for example after Blerg was updated.'
export const SHELL_NO_LONGER_RUNNING = 'It is no longer running, for example after Blerg was updated.'
// Causes that say the shell's authorization (its lease or grant) is gone, rather than its helper failing.
const authorizationCauses = new Set(['lease_check', 'lease_create', 'control_grant', 'verify_grant', 'verify_policy', 'verify_binding', 'verify_reservation'])
/** Why a live failure ended the shell: the authorization line for a lease refusal, else its fixed sentence. */
function failureEnded(cause: unknown, message: string): string {
  return `${SHELL_ENDED} ${typeof cause === 'string' && authorizationCauses.has(cause) ? SHELL_AUTHORIZATION_EXPIRED : message}`
}
/**
 * An unexpected drop reconnects by itself: the first try after RECOVERY_FIRST_MS, doubling up to
 * RECOVERY_MAX_MS between tries, and it gives up once RECOVERY_BUDGET_MS have passed.
 */
export const RECOVERY_FIRST_MS = 500
export const RECOVERY_MAX_MS = 10000
export const RECOVERY_BUDGET_MS = 60000
/** Each try's deadlines: the inventory request, then the socket (token renewal included). */
export const RECOVERY_PROBE_MS = 5000
export const RECOVERY_OPEN_MS = 10000
const after = (ms: number) => new Promise<void>(resolve => setTimeout(resolve, ms))

// The server allows 16 KiB and 128 input frames per second per session; the
// client stays under 12 KiB and 64 frames in any sliding second for jitter room.
export const SHELL_INPUT_BYTES_PER_SECOND = 12 << 10
export const SHELL_INPUT_FRAMES_PER_SECOND = 64
const CHUNK = 4096

// One bounded input scheduler for the whole session, including all splits and pop-out panes.
// Input goes out at once while the sliding window has room, so typing never waits; only
// bursts past the budget queue, and queued input for the same shell coalesces into one frame.
export class ShellRateGroup {
  private queue: { owner: WebSocketShell; data: Uint8Array; human: boolean }[] = []
  private bytes = 0
  private usedBytes = 0
  private usedFrames = 0
  enqueue(owner: WebSocketShell, data: Uint8Array, human = false) {
    if (this.bytes + data.length > 65536) { owner.disconnect(); return }
    for (let offset = 0; offset < data.length; offset += CHUNK) {
      const chunk = data.slice(offset, offset + CHUNK)
      const last = this.queue[this.queue.length - 1]
      if (last && last.owner === owner && last.human === human && last.data.length + chunk.length <= CHUNK) {
        const merged = new Uint8Array(last.data.length + chunk.length)
        merged.set(last.data); merged.set(chunk, last.data.length); last.data = merged
      } else this.queue.push({ owner, data: chunk, human })
      this.bytes += chunk.length
    }
    this.flush()
  }
  remove(owner: WebSocketShell) {
    this.queue = this.queue.filter(item => item.owner !== owner)
    this.bytes = this.queue.reduce((n, item) => n + item.data.length, 0)
  }
  private flush() {
    while (this.queue.length) {
      const item = this.queue[0], n = item.data.length
      if (this.usedFrames >= SHELL_INPUT_FRAMES_PER_SECOND || this.usedBytes + n > SHELL_INPUT_BYTES_PER_SECOND) return
      this.queue.shift(); this.bytes -= n
      this.usedBytes += n; this.usedFrames++
      // Each send holds its share of the budget for one second, then retries the queue.
      setTimeout(() => { this.usedBytes -= n; this.usedFrames--; this.flush() }, 1000)
      item.owner.sendInput(item.data, item.human)
    }
  }
}

// The server keeps each shell's last 256 KiB of output (ownershell.ScrollbackBytes)
// and, when asked, replays it at the start of every socket before live output.
export const SHELL_SCROLLBACK_BYTES = 256 << 10
// Full terminal reset (RIS). The server's replay is the whole kept history, so
// the terminal starts from it: whatever this page showed before is superseded.
const TERMINAL_RESET = new Uint8Array([0x1b, 0x63])
// Displayed output a session view keeps when it unmounts, so returning to the session
// shows the shell's recent scrollback before live output resumes. Page memory only:
// never storage, never output nobody saw, and never across a sign-in change.
export const SHELL_REPLAY_BYTES = 128 << 10
const RETAINED_SHELLS = 64
const retained = new Map<string, { session: string; shell: WebSocketShell }>()
// Bumped whenever kept shells are cleared, so a restore or open that started before
// the clear cannot repopulate the map with the old authority's transports.
let retentionEpoch = 0
function retainedKey(owner: string, info: ShellInfo) { return `${owner}\n${info.handle}\n${info.shell_id}` }
function retain(session: string, shell: WebSocketShell) {
  const key = retainedKey(shell.owner, shell.info)
  retained.delete(key); retained.set(key, { session, shell })
  while (retained.size > RETAINED_SHELLS) { const [oldest] = retained.keys(); retained.get(oldest)?.shell.forget(); retained.delete(oldest) }
}
/** Called with a transport whose scrollback was erased, or with nothing when all of them were. */
const forgotten = new Set<(shell?: ShellTransport) => void>()
/** A kept terminal (lib/keptTerminals) follows its transport's scrollback: erased, it goes too. */
export function onShellForgotten(listener: (shell?: ShellTransport) => void): () => void {
  forgotten.add(listener)
  return () => { forgotten.delete(listener) }
}
/** The login a shell transport belongs to now (account and login session). */
export function shellLoginIdentity(): string { return loginIdentity(getAccessToken()) }
/**
 * The kept transports of a session for the signed-in login, as a returning view draws them
 * before its inventory answers (card #281); the inventory then adopts and reconnects them.
 */
export function retainedShellsFor(session: string): WebSocketShell[] {
  const login = loginIdentity(getAccessToken())
  return [...retained.values()].filter(entry => entry.session === session && entry.shell.owner === login).map(entry => entry.shell)
}
/**
 * Erases a session's kept transports and their scrollback (it was evicted from the page's
 * retention, or ended, was archived or discarded): its kept terminals go with them (onShellForgotten),
 * and a transport no view shows is disconnected. Called only for a session not on screen.
 */
export function forgetRetainedSession(session: string) {
  for (const entry of [...retained.values()]) {
    if (entry.session !== session) continue
    entry.shell.forget()
    if (!entry.shell.subscribed()) entry.shell.disconnect()
  }
}
function retainedShell(session: string, info: ShellInfo): WebSocketShell | undefined {
  const entry = retained.get(retainedKey(loginIdentity(getAccessToken()), info))
  return entry?.session === session ? entry.shell : undefined
}
/**
 * Erases every kept shell transport and its scrollback. A sign-in change (account or
 * login session) or a confirmed sign-out also disconnects and closes them, attached
 * or not; a desktop scope change drops detached ones, like the session view cache.
 */
// Shells an inactivity limit ended keep their last screen for the login that owned them, even
// once nothing retains them for replay; a sign-in change or sign-out must still erase it.
const limitEnded = new Set<WebSocketShell>()
export function forgetRetainedShells(reason: 'login' | 'desktop' = 'login') {
  retentionEpoch++
  if (reason === 'login') { for (const shell of [...limitEnded]) shell.revoke(); limitEnded.clear() }
  for (const { shell } of [...retained.values()]) {
    shell.forget()
    if (reason === 'login') shell.revoke()
    else if (!shell.subscribed()) shell.disconnect()
  }
  retained.clear()
  for (const listener of [...forgotten]) listener()
}
if (typeof window !== 'undefined') {
  for (const event of ['blerg-identity-changed', 'blerg-login-changed', 'blerg-signed-out']) window.addEventListener(event, () => forgetRetainedShells('login'))
  window.addEventListener('blerg-desktop-scope-changed', () => forgetRetainedShells('desktop'))
}
/**
 * One restore or open attempt's claim on a transport. A session view keeps one rate
 * group for its whole mount, but it can restore more than once (a capability refresh
 * restarts its effect), so the group cannot tell an abandoned attempt from its
 * replacement. Each attempt gets its own lease; the newest adoption holds the transport.
 */
export class ShellLease {}
/**
 * Disconnects only what the caller still owns. With a lease (an abandoned restore or
 * open), only if no later attempt, in this view or a returning one, adopted the
 * transport since. With a rate group (a view going away), only if no other view did.
 */
export function disconnectOwned(shell: ShellTransport, owner: ShellRateGroup | ShellLease) {
  if (shell instanceof WebSocketShell) shell.release(owner)
  else shell.disconnect()
}
function unretain(shell: WebSocketShell) {
  const key = retainedKey(shell.owner, shell.info)
  if (retained.get(key)?.shell === shell) retained.delete(key)
}

function loginIdentity(token: string | null): string {
  if (!token) return ''
  try { const p = JSON.parse(atob(token.split('.')[1].replace(/-/g, '+').replace(/_/g, '/'))) as { sub?: string; sid?: string; kind?: string }; return `${p.kind}:${p.sub}:${p.sid}` }
  catch { return token }
}
export class WebSocketShell implements ShellTransport {
  private socket: WebSocket | undefined
  private output: ((bytes: Uint8Array, replay?: boolean) => void) | undefined
  /**
   * This socket's server replay: none seen yet ('unseen'), its bytes still arriving
   * ('active'), or over ('done': finished, empty, or live output came first). At most
   * one notice per socket, and only before any live output; a new socket starts over.
   */
  private replayState: 'unseen' | 'active' | 'done' = 'unseen'
  /** Bytes of the server's replay still to come on this socket. */
  private replayLeft = 0
  /**
   * A server replay that arrived before any view subscribed (a socket opens before
   * its view mounts). Held, bounded by the notice (at most SHELL_SCROLLBACK_BYTES),
   * for the first subscriber; cleared with this transport's scrollback by forget().
   */
  private pendingReplay: Uint8Array[] | null = null
  /**
   * Set when this transport's scrollback was forgotten while a server replay was still
   * arriving: the rest of that replay is counted (the socket's notice guard holds) but
   * neither kept nor shown, so erased history cannot grow back from its tail.
   */
  private replayDiscarded = false
  private failed: ((message: string) => void) | undefined
  private failureMessage = ''
  private ended = ''
  private changed: ((state: ShellState) => void) | undefined
  private state: ShellState = 'disconnected'
  private heartbeat: ReturnType<typeof setInterval> | undefined
  private lastActivity = 0
  private inputSeq = 0
  private resizeSeq = 0
  private generation = 0
  private identity = ''
  private history: Uint8Array[] = []
  private historyBytes = 0
  readonly info: ShellInfo
  readonly owner: string
  private group: ShellRateGroup
  private lease: ShellLease
  /** The session whose inventory says what became of this shell after a drop; none: no reconnect by itself. */
  readonly session: string
  /** Bumped by every explicit connect or disconnect: a reconnect in progress by itself stops. */
  private recovery = 0
  private recovering = false
  private why: 'elsewhere' | 'outage' | undefined
  /** Settles the current socket attempt: true once it opened, false once it failed. */
  private settle: ((opened: boolean) => void) | undefined
  constructor(info: ShellInfo, group: ShellRateGroup, lease = new ShellLease(), session = '') { this.info = info; this.group = group; this.lease = lease; this.session = session; this.owner = loginIdentity(getAccessToken()) }
  /**
   * A restore or open attempt takes ownership with its own lease; its view shares one
   * input budget across all of its shells. Only the current owner's cleanup can
   * disconnect the transport.
   */
  adopt(group: ShellRateGroup, lease = new ShellLease()) {
    if (group !== this.group) { this.group.remove(this); this.group = group }
    this.lease = lease
  }
  ownedBy(owner: ShellRateGroup | ShellLease) { return owner instanceof ShellLease ? this.lease === owner : this.group === owner }
  /** Disconnects for an attempt or view that is going away, unless a newer one adopted this transport. */
  release(owner: ShellRateGroup | ShellLease) { if (this.ownedBy(owner)) this.disconnect() }
  subscribed() { return !!this.output }
  /** Drops kept scrollback; the shell ended, the sign-in changed, or the session no longer lists it. */
  forget() {
    this.history = []; this.historyBytes = 0; this.pendingReplay = null
    if (this.replayState === 'active') this.replayDiscarded = true
    unretain(this)
    // A kept terminal (lib/keptTerminals) shows this scrollback: it goes with it.
    for (const listener of [...forgotten]) listener(this)
  }
  /** The sign-in that owned this transport is gone: close it for good. */
  revoke() { this.forget(); this.failureMessage = ''; this.ended = ''; limitEnded.delete(this); this.disconnect(); this.status('closed') }
  private remember(bytes: Uint8Array) {
    this.history.push(bytes); this.historyBytes += bytes.byteLength
    while (this.historyBytes > SHELL_REPLAY_BYTES && this.history.length > 1) this.historyBytes -= this.history.shift()!.byteLength
  }
  subscribe(output: (bytes: Uint8Array, replay?: boolean) => void, state: (state: ShellState) => void, error?: (message: string) => void) {
    this.output = output; this.changed = state; this.failed = error; state(this.state)
    // Each subscriber is a fresh terminal: give it what the previous one displayed,
    // marked as replay so its historical terminal queries are not answered again.
    // A server replay nobody has received yet supersedes that: its reset, then its bytes.
    if (this.pendingReplay) {
      const pending = this.pendingReplay
      this.pendingReplay = null
      output(TERMINAL_RESET, true)
      for (const bytes of pending) { this.remember(bytes); output(bytes, true) }
    } else for (const bytes of this.history) output(bytes, true)
    if (this.failureMessage) error?.(this.failureMessage)
    return () => { if (this.output === output) { this.output = undefined; this.changed = undefined; this.failed = undefined } }
  }
  private status(state: ShellState) { this.state = state; this.changed?.(state) }
  reconnecting() { return this.recovering }
  disconnectedReason() { return this.state === 'disconnected' ? this.why : undefined }
  /** The session's inventory says another tab holds this shell. */
  controlledElsewhere() { this.why = 'elsewhere'; if (this.state === 'disconnected') this.status('disconnected') }
  async reconnect(takeover = false, signal?: AbortSignal) {
    this.recovery++; this.recovering = false
    await this.connect(takeover, signal)
  }
  /**
   * Opens a socket. Resolves once it is created (or the attempt ended before that); `opened` then
   * settles with the socket: true once it opened, false once it failed or was superseded.
   */
  private async connect(takeover = false, signal?: AbortSignal): Promise<{ opened: Promise<boolean> }> {
    this.teardown()
    this.failureMessage = ''
    this.why = undefined
    let settle!: (opened: boolean) => void
    const opened = new Promise<boolean>(resolve => { settle = resolve })
    this.settle = settle
    const generation = ++this.generation
    const owner = this.lease
    // A sign-out, sign-in or desktop change while this waits fences it: no socket is opened (#281).
    const epoch = retentionEpoch
    this.status('connecting')
    const renewed = await ensureFreshToken()
    // A newer reconnect or disconnect owns the transport now: never touch it.
    if (generation !== this.generation) { settle(false); return { opened } }
    // The renewal failed (no sign-in to open a socket with) or the authority changed meanwhile.
    if (!renewed || epoch !== retentionEpoch) { settle(false); this.status('disconnected'); return { opened } }
    if (signal?.aborted) {
      // The attempt that asked went away. Disconnect only if no newer attempt adopted
      // the transport; otherwise leave it for its owner, without a half-open state.
      settle(false)
      if (this.lease === owner) this.disconnect(); else this.status('disconnected')
      return { opened }
    }
    const token = getAccessToken()
    if (!token) { settle(false); this.status('disconnected'); return { opened } }
    const url = new URL(`/api/shells/${encodeURIComponent(this.info.handle)}/socket`, location.origin)
    url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
    if (takeover) url.searchParams.set('takeover', 'true')
    url.searchParams.set('replay', '1')
    this.identity = loginIdentity(token)
    const socket = new WebSocket(url, ['bearer', token]); socket.binaryType = 'arraybuffer'
    this.socket = socket; this.inputSeq = 0; this.resizeSeq = 0; this.replayState = 'unseen'; this.replayLeft = 0; this.pendingReplay = null; this.replayDiscarded = false
    socket.onopen = () => {
      if (generation !== this.generation) { socket.close(); return }
      // The authority changed while the socket opened: it is closed, never used.
      if (epoch !== retentionEpoch || !this.sameIdentity()) { this.disconnect(); return }
      this.settle = undefined; this.recovering = false; settle(true)
      this.status('running')
      // Publishing 'running' sends held keys, and that can disconnect (a full input queue): then this
      // socket is no longer current, and nothing of it (no heartbeat) may outlive that disconnect.
      if (generation !== this.generation || this.state !== 'running') return
      this.heartbeat = setInterval(() => { if (generation === this.generation) this.send({ type: 'heartbeat' }) }, 15000)
    }
    socket.onmessage = event => {
      if (generation !== this.generation || !this.sameIdentity()) return
      if (typeof event.data === 'string') {
        try {
          const frame = JSON.parse(event.data) as { type?: string; phase?: string; cause?: unknown; bytes?: unknown; reason?: unknown; after_seconds?: unknown }
          const ended = frame.type === 'ended' ? shellEndedMessage(frame.reason, frame.after_seconds) : undefined
          if (ended) {
            // An ended shell has no replay: its kept scrollback goes (a later view starts
            // blank), while the terminal showing it keeps its last screen for this login.
            this.ended = ended; limitEnded.add(this)
            this.forget(); this.disconnect(); this.status('closed')
            return
          }
          if (frame.type === 'error') {
            // The server closes a shell after every live failure it reports: it ended, and says why.
            this.failureMessage = shellFailureMessage(frame.cause) ?? 'The shell stopped. Check the desktop daemon log, then open a new shell.'
            this.gone(failureEnded(frame.cause, this.failureMessage))
            this.failed?.(this.failureMessage)
            return
          }
          if (frame.type === 'phase' && (frame.phase === 'syncing' || frame.phase === 'running')) {
            this.status(frame.phase)
            return
          }
          // The kept scrollback comes first, once per socket: exactly `bytes` bytes.
          if (frame.type === 'replay' && Number.isInteger(frame.bytes) && (frame.bytes as number) >= 0 && (frame.bytes as number) <= SHELL_SCROLLBACK_BYTES && this.replayState === 'unseen') {
            this.replayLeft = frame.bytes as number
            this.replayState = this.replayLeft > 0 ? 'active' : 'done'
            // The terminal restarts from the server's history, so this page's kept
            // scrollback restarts with it (a later remount replays the same screen).
            this.history = []; this.historyBytes = 0
            if (this.output) this.output(TERMINAL_RESET, true)
            else this.pendingReplay = []
            return
          }
        } catch { /* Invalid control closes this dedicated channel. */ }
        this.disconnect(); return
      }
      if (!(event.data instanceof ArrayBuffer) || event.data.byteLength > 16384) { this.disconnect(); return }
      const bytes = new Uint8Array(event.data)
      if (this.replayState === 'active') {
        // The server never sends an empty replay chunk; one would make the held replay
        // grow without counting toward its notice.
        if (bytes.byteLength === 0 || bytes.byteLength > this.replayLeft) { this.disconnect(); return }
        this.replayLeft -= bytes.byteLength
        if (this.replayLeft === 0) this.replayState = 'done'
        if (this.replayDiscarded) return
        // Replay is history, not unseen live output: kept for the first subscriber.
        if (this.pendingReplay) this.pendingReplay.push(bytes)
        else { this.remember(bytes); this.output?.(bytes, true) }
        return
      }
      this.replayState = 'done'  // live output first: no replay may follow on this socket
      if (!this.output) return
      this.remember(bytes)
      this.output(bytes)
    }
    // A close nobody asked for (a server restart, a refused socket, another tab's takeover, a
    // network blip): what the server lists now decides what follows (recover()).
    socket.onclose = () => { this.dropped(generation) }
    socket.onerror = () => { this.dropped(generation) }
    return { opened }
  }
  /** The current socket closed or failed without this page asking. */
  private dropped(generation: number) {
    if (generation !== this.generation) return
    const settle = this.settle
    this.teardown()
    // A try of a reconnect in progress failed: that reconnect goes on.
    if (settle) { this.settle = undefined; settle(false); if (this.recovering) return }
    if (this.state === 'closed') return
    // Only a shell of a known session reconnects by itself; anything else waits for a click. A view
    // that leaves disconnects its shells, which stops a reconnect in progress.
    if (!this.session) { this.status('disconnected'); return }
    void this.recover()
  }
  /**
   * Reconnects after a drop, with a bounded backoff, while the picture stays (the state stays
   * 'connecting', so typing is held). Before each try the session's inventory says what became of
   * the shell: not listed, it ended; attached elsewhere, another tab controls it (never taken over
   * by itself); listed and free, a new socket. Gives up as Disconnected after RECOVERY_BUDGET_MS.
   */
  private async recover() {
    const token = ++this.recovery
    this.recovering = true
    this.status('connecting')
    // The budget is real time: waits and tries alike. Each try has deadlines within what is left.
    const started = Date.now()
    const left = () => RECOVERY_BUDGET_MS - (Date.now() - started)
    let why: 'elsewhere' | 'outage' = 'outage'
    for (let delay = RECOVERY_FIRST_MS; delay < left(); delay = Math.min(RECOVERY_MAX_MS, delay * 2)) {
      await after(delay)
      if (token !== this.recovery) return
      const found = await this.probe(Math.min(RECOVERY_PROBE_MS, left()))
      if (token !== this.recovery) return
      if (found === 'gone') { this.recovering = false; this.gone(`${SHELL_ENDED} ${SHELL_NO_LONGER_RUNNING}`); return }
      if (found === 'attached') { why = 'elsewhere'; break }
      if (found === 'unknown') continue
      const opened = await Promise.race([this.connect().then(attempt => attempt.opened), after(Math.max(0, Math.min(RECOVERY_OPEN_MS, left()))).then(() => false)])
      if (token !== this.recovery) return
      if (opened) return
      // A socket (or token renewal) that has not answered by its deadline is abandoned.
      this.teardown()
    }
    this.recovering = false
    this.recovery++
    this.why = why
    this.status('disconnected')
  }
  /** What the session's inventory says about this shell now. */
  private async probe(deadline: number): Promise<'free' | 'attached' | 'gone' | 'unknown'> {
    const abort = new AbortController()
    const ask = (async () => {
      try {
        const response = await apiFetch(`/api/sessions/${encodeURIComponent(this.session)}/shells`, { signal: abort.signal })
        if (!response.ok) return 'unknown' as const
        const infos = await response.json() as unknown
        if (!Array.isArray(infos)) return 'unknown' as const
        const listed = (infos as Partial<ShellInfo>[]).find(info => info?.handle === this.info.handle && info?.shell_id === this.info.shell_id)
        return !listed ? 'gone' as const : listed.attached ? 'attached' as const : 'free' as const
      } catch { return 'unknown' as const }
    })()
    // An inventory that has not answered by its deadline is abandoned (and its request aborted).
    const late = after(Math.max(0, deadline)).then(() => { abort.abort(); return 'unknown' as const })
    return Promise.race([ask, late])
  }
  /** The shell no longer runs: its last screen stays for this login (like a limit-ended one) with why. */
  private gone(message: string) {
    this.ended = message; limitEnded.add(this)
    this.forget(); this.disconnect(); this.status('closed')
  }
  private sameIdentity(): boolean {
    if (!this.identity || loginIdentity(getAccessToken()) !== this.identity) { this.forget(); this.disconnect(); this.status('closed'); return false }
    return true
  }
  private send(frame: object) {
    if (!this.sameIdentity()) return
    // A socket that is no longer open went away without this page asking; a full buffer is this page's.
    if (this.socket?.readyState !== WebSocket.OPEN) { this.dropped(this.generation); return }
    if (this.socket.bufferedAmount > 65536) { this.disconnect(); return }
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
    this.recovery++; this.recovering = false
    this.teardown()
    if (this.state !== 'closed') this.status('disconnected')
  }
  /** Closes the current socket and fences its handlers; the state is the caller's. */
  private teardown() {
    this.generation++; this.group.remove(this)
    if (this.heartbeat) clearInterval(this.heartbeat)
    this.heartbeat = undefined
    const settle = this.settle; this.settle = undefined; settle?.(false)
    const old = this.socket; this.socket = undefined
    old?.close()
  }
  endedMessage() { return this.ended }
  async end() {
    const response = await apiFetch(`/api/shells/${encodeURIComponent(this.info.handle)}`, { method: 'DELETE' })
    if (!response.ok) throw new Error('Shell could not be ended.')
    this.forget(); this.disconnect(); this.status('closed')
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
export async function openShell(session: string, group: ShellRateGroup, lease = new ShellLease()): Promise<ShellTransport> {
  const login = loginIdentity(getAccessToken())
  // Taken before the first wait: a sign-out, sign-in or desktop change while the shell is being
  // created fences what comes back (no transport is kept, no socket opened).
  const epoch = retentionEpoch
  const fenced = () => epoch !== retentionEpoch || loginIdentity(getAccessToken()) !== login
  const response = await apiFetch(`/api/sessions/${encodeURIComponent(session)}/shells`, { method: 'POST' })
  if (response.status === 428) {
    const challenge = await apiFetch(`/api/sessions/${encodeURIComponent(session)}/shell-consent/challenge`, { method: 'POST' })
    if (!challenge.ok) throw new Error('Host shell challenge unavailable.')
    throw new HostShellConsentRequired(await challenge.json() as HostShellChallenge)
  }
  if (!response.ok) {
    if (response.status === 429) throw new Error('This session already has four shells.')
    const body = await response.json().catch(() => null) as { code?: unknown; cause?: unknown } | null
    const code = typeof body?.code === 'string' ? body.code : ''
    const messages: Record<string, string> = {
      session_not_ready: 'This session is still resuming. Wait for recovery, then open the shell again.',
      policy_disabled: 'Owner shells are disabled. Ask an administrator to enable them in Settings.',
      policy_unavailable: 'Shell policy is unavailable. Wait for the server to reconnect, then try again.',
      authority_refused: 'Your shell access could not be verified. Sign in again, then try again.',
      binding_refused: 'The session runtime changed. Reconnect the desktop, then open the shell again.',
      runtime_binding_refused: 'The session runtime changed. Reconnect the desktop, then open the shell again.',
      runtime_consent_refused: 'Consent no longer matches this launch. Open the shell again to review the current machine and account.',
      runtime_identity_refused: 'The host account or project directory changed. Restore the original directory, then reconnect the desktop and review consent again.',
      runtime_cleanup_refused: 'Shell cleanup could not be verified. Reconnect the desktop and check its daemon log before trying again.',
      runtime_fenced: 'The shell is paused after a failed start or while the session recovers. Wait a few seconds and try again. If it keeps failing, reconnect the desktop or resume the session.',
      runtime_capability_refused: 'This desktop does not support the required shell containment. Update the desktop and reconnect it.',
      runtime_policy_refused: 'Shell policy changed. Reconnect the desktop, then try again.',
      runtime_busy: 'The desktop is busy opening other session workspaces. Try again in a moment.',
      runtime_start_refused: 'The desktop could not start the contained shell. Check the desktop daemon log, then reconnect the desktop and try again.',
    }

    const causeMessage = shellFailureMessage(body?.cause)
    if (code === 'runtime_start_refused' && causeMessage) throw new Error(causeMessage)
    throw new Error((Object.hasOwn(messages, code) ? messages[code] : undefined) ?? 'Shell is unavailable. Check the desktop daemon log, then reconnect the desktop and try again.')
  }
  if (fenced()) throw new Error('Your sign-in changed. Open the shell again.')
  const info = await response.json() as ShellInfo
  if (fenced()) throw new Error('Your sign-in changed. Open the shell again.')
  const shell = new WebSocketShell(info, group, lease, session)
  if (shell.owner !== login) throw new Error('Your sign-in changed. Open the shell again.')
  retain(session, shell)
  await shell.reconnect()
  if (fenced()) { shell.forget(); shell.disconnect(); throw new Error('Your sign-in changed. Open the shell again.') }
  return shell
}

export async function restoreShells(session: string, group: ShellRateGroup, signal?: AbortSignal, existing: ShellTransport[] = [], closedIds: string[] = [], lease = new ShellLease()): Promise<ShellTransport[]> {
  const login = loginIdentity(getAccessToken())
  // Kept shells of another sign-in are erased, whether or not an event announced the change.
  if ([...retained.values()].some(entry => entry.shell.owner !== login)) forgetRetainedShells('login')
  const epoch = retentionEpoch
  const fenced = () => epoch !== retentionEpoch || loginIdentity(getAccessToken()) !== login
  const response = await apiFetch(`/api/sessions/${encodeURIComponent(session)}/shells`, { signal })
  if (!response.ok) throw new Error('Shell inventory unavailable.')
  const infos = await response.json() as ShellInfo[]
  if (!Array.isArray(infos) || infos.length > 4 || infos.some(info => !info || typeof info.shell_id !== 'string' || typeof info.handle !== 'string' || !['host', 'sandbox', 'cluster'].includes(info.runtime) || typeof info.folder !== 'string')) throw new Error('Invalid shell inventory.')
  if (signal?.aborted) return []
  // The kept shells were cleared (sign-in or desktop change) while this list was in flight.
  if (fenced()) return []
  // The session lists every live shell of this login: anything kept for it and not listed has ended.
  for (const entry of [...retained.values()]) if (entry.session === session && entry.shell.owner === login && !infos.some(info => info.handle === entry.shell.info.handle && info.shell_id === entry.shell.info.shell_id)) entry.shell.forget()
  const transports = infos.map(info => {
    const own = existing.find(shell => shell.info.handle === info.handle && shell.info.shell_id === info.shell_id)
    if (own) return own
    // A session view that unmounted (the person left the session) left its shells here.
    const kept = retainedShell(session, info)
    const shell = kept ?? new WebSocketShell(info, group, lease, session)
    shell.adopt(group, lease)
    retain(session, shell)
    return shell
  })
  await Promise.all(transports.map(async (shell, index) => {
    // A capability refresh inventories the page's own attached shells too. Reuse
    // their live socket and subscriber; attached does not mean another browser.
    if (existing.includes(shell) || closedIds.includes(`shell:${shell.info.shell_id}`)) return
    try {
      if (!infos[index].attached && shell instanceof WebSocketShell) await shell.reconnect(false, signal)
      else if (infos[index].attached && shell instanceof WebSocketShell) shell.controlledElsewhere()
      if (signal?.aborted) disconnectOwned(shell, lease)
    } catch { disconnectOwned(shell, lease) }
  }))
  if (fenced()) {
    transports.forEach(shell => { if (!existing.includes(shell) && shell instanceof WebSocketShell) { shell.forget(); shell.release(lease) } })
    return []
  }
  return transports
}
