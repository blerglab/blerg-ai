// keptTerminals: a shell's terminal (its xterm instance, what it displays and its subscription to
// the shell transport) outlives the session view that showed it, for the sessions viewed most
// recently, so coming back to a session shows its shells at once instead of a blank surface
// filling in (card #281). A kept terminal is detached from the page (not disposed); it stays
// subscribed to its transport, so it never replays scrollback it already shows.
//
// - Bounded: the terminals of the last KEPT_TERMINAL_SESSIONS sessions viewed; older ones are
//   disposed (a later visit draws a fresh terminal from the transport's kept scrollback, #257).
// - One sign-in's: a sign-in change, sign-out or desktop scope change disposes every kept terminal,
//   as it erases the transports' scrollback (shellTransport.forgetRetainedShells); a transport
//   that forgets its scrollback (its shell ended, the session no longer lists it) disposes its
//   terminal too. A terminal kept under another login is never handed out.
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { onShellForgotten, shellLoginIdentity, type ShellState, type ShellTransport } from '../shellTransport'
import { shellPaletteShortcut } from '../commands/shortcutOwnership'
import { shellTheme } from './shellTheme'

/** How many sessions' terminals are kept detached. */
export const KEPT_TERMINAL_SESSIONS = 3

/** The view a terminal is shown in, while attached. */
export interface TerminalView {
  setState(state: ShellState): void
  /** Output reached the terminal. */
  onOutput?(): void
  onError(message: string): void
  /** The terminal closed after showing output: the view draws a fresh surface. */
  retired(): void
}

export interface ShellTerminal {
  readonly transport: ShellTransport
  readonly disposed: boolean
  /** Whether it has displayed any output. */
  readonly wrote: boolean
  readonly state: ShellState
  attach(root: HTMLElement, view: TerminalView): void
  detach(): void
  dispose(): void
  fit(): void
  focus(): void
}

interface Kept { terminal: ShellTerminal; session: string; owner: string }
const kept = new Map<ShellTransport, Kept>()

/** A kept terminal goes: its xterm is disposed, and its transport, once no view shows it any more,
 *  is disconnected (a newer view that shows it keeps it). */
function discard(entry: Kept) {
  kept.delete(entry.terminal.transport)
  entry.terminal.dispose()
  const transport = entry.terminal.transport as ShellTransport & { subscribed?: () => boolean }
  if (!transport.subscribed?.()) transport.disconnect()
}

/** The kept terminal of this transport (taken out of the kept set), or a new one. */
export function terminalFor(transport: ShellTransport): ShellTerminal {
  const entry = kept.get(transport)
  if (entry) {
    kept.delete(transport)
    if (entry.owner === shellLoginIdentity() && !entry.terminal.disposed) return entry.terminal
    discard(entry)
  }
  return createTerminal(transport)
}

/** Keeps a detached terminal for the session; the oldest sessions' terminals past the bound go. */
export function keepTerminal(terminal: ShellTerminal, session: string): void {
  if (terminal.disposed) return
  kept.delete(terminal.transport)
  kept.set(terminal.transport, { terminal, session, owner: shellLoginIdentity() })
  const sessions: string[] = []
  for (const entry of kept.values()) { const i = sessions.indexOf(entry.session); if (i >= 0) sessions.splice(i, 1); sessions.push(entry.session) }
  const evicted = new Set(sessions.slice(0, Math.max(0, sessions.length - KEPT_TERMINAL_SESSIONS)))
  for (const entry of [...kept.values()]) if (evicted.has(entry.session)) discard(entry)
}

/** Disposes kept terminals: all of them, or one session's. */
export function disposeKeptTerminals(session?: string): void {
  for (const entry of [...kept.values()]) {
    if (session !== undefined && entry.session !== session) continue
    discard(entry)
  }
}

/** The kept terminals now, by session (tests and memory checks). */
export function keptTerminalSessions(): string[] {
  return [...new Set([...kept.values()].map(entry => entry.session))]
}

onShellForgotten(transport => {
  if (transport === undefined) { disposeKeptTerminals(); return }
  const entry = kept.get(transport)
  if (entry) discard(entry)
})
if (typeof window !== 'undefined') {
  for (const event of ['blerg-identity-changed', 'blerg-login-changed', 'blerg-signed-out', 'blerg-desktop-scope-changed']) window.addEventListener(event, () => disposeKeptTerminals())
}

/** At most this much typing is held while a kept terminal reconnects; the rest is dropped. */
const HELD_INPUT_MAX = 4096

function createTerminal(transport: ShellTransport): ShellTerminal {
  // The terminal draws into a node of its own, moved between the session views that show it.
  const node = document.createElement('div')
  node.className = 'shell-terminal-host'
  node.style.width = '100%'
  node.style.height = '100%'
  const term = new Terminal({ scrollback: 2000, screenReaderMode: true, fontSize: 13, ...shellTheme(node), convertEol: false, disableStdin: true })
  let view: TerminalView | null = null
  let root: HTMLElement | null = null
  let state: ShellState = 'connecting'
  // Output handed to xterm is parsed and painted later, asynchronously, and reset()
  // does not cancel it. When the shell closes (ended, or its sign-in is gone and the
  // kept scrollback was erased), a terminal that was given output is disposed at once,
  // with its queued writes, so none of them can paint onto the closed surface.
  let wrote = false, disposed = false
  const fit = new FitAddon(); term.loadAddon(fit); term.open(node)
  // All terminal keys reach the PTY except the platform's palette and tab focus bindings.
  term.attachCustomKeyEventHandler(event => !event.defaultPrevented && !shellPaletteShortcut(event))
  const media = matchMedia('(prefers-color-scheme: light)')
  const applyTheme = () => { if (disposed || !root) return; const options = shellTheme(root); term.options.theme = options.theme; term.options.fontFamily = options.fontFamily; fitTerminal() }
  const themeObserver = new MutationObserver(applyTheme)
  // Do not permit OSC clipboard, window operations, or titles to escape the shell surface.
  const osc52 = term.parser.registerOscHandler(52, () => true)
  const encoder = new TextEncoder()
  // xterm also emits automatic terminal-query replies through onData. Only DOM human
  // input events can mark bytes as activity; output parsing has no such authority.
  let humanEvent: Event | undefined
  const activity = (event: Event) => { humanEvent = event; transport.activity?.() }
  const events = ['keydown', 'keypress', 'beforeinput', 'paste', 'compositionstart', 'compositionupdate', 'compositionend', 'input']
  events.forEach(event => node.addEventListener(event, activity, true))
  // Replayed scrollback was already answered by the terminal that first displayed it.
  // Until xterm has parsed all of it (writes are asynchronous; the callbacks run in
  // order, after each chunk is parsed), terminal-generated replies are dropped; only
  // input from a DOM event the person caused still reaches the shell.
  let replaying = 0
  const input = term.onData(data => {
    // A detached terminal answers nothing: not even xterm's own replies to a terminal query that
    // reaches it while kept. Only a terminal on screen sends input.
    if (!root) return
    const human = !!humanEvent && humanEvent.eventPhase !== Event.NONE
    if (replaying > 0 && !human) return
    // A terminal that shows its picture reconnects quietly (ShellView): what the person types
    // meanwhile is held, bounded, and sent once the shell runs. Terminal replies are not held.
    if (state === 'connecting') {
      if (!human) return
      const bytes = encoder.encode(data)
      if (heldBytes + bytes.byteLength <= HELD_INPUT_MAX) { held.push(bytes); heldBytes += bytes.byteLength }
      return
    }
    transport.input(encoder.encode(data), human)
  })
  let held: Uint8Array[] = [], heldBytes = 0
  const dropHeld = () => { held = []; heldBytes = 0 }
  let pending = 0
  let frame = 0
  let resizeTimer: ReturnType<typeof setTimeout> | undefined
  let lastSize = ''
  let running = false
  const sendSize = () => {
    resizeTimer = undefined
    const size = `${term.cols}:${term.rows}`
    if (running && size !== lastSize) { transport.resize(term.cols, term.rows); lastSize = size }
  }
  function fitTerminal() {
    cancelAnimationFrame(frame)
    if (disposed) return
    frame = requestAnimationFrame(() => {
      if (disposed || !root || !node.isConnected || node.clientWidth < 20 || node.clientHeight < 20) return
      fit.fit()
      // Resize observers can fire for xterm's own layout, and pointer drags can
      // exceed the session's shared control quota. Send changed geometry only,
      // at most five times per second per shell, including the trailing size.
      if (!lastSize) sendSize()
      else if (resizeTimer === undefined) resizeTimer = setTimeout(sendSize, 200)
    })
  }
  const observer = new ResizeObserver(fitTerminal)
  const unsubscribe = transport.subscribe((bytes, replay) => {
    if (disposed) return
    wrote = true
    if (bytes.byteLength > 0) view?.onOutput?.()
    // Replay is bounded where it comes from (the server's one notice of at most its kept
    // scrollback, or this page's memory, #268); the pending cap guards live output.
    if (replay) {
      replaying++
      term.write(bytes, () => { replaying-- })
      return
    }
    if (pending + bytes.byteLength > 262144) { transport.disconnect(); return }
    pending += bytes.byteLength
    term.write(bytes, () => { pending -= bytes.byteLength })
  }, next => {
    state = next
    running = next === 'running'; if (!running) lastSize = ''
    term.options.disableStdin = !(next === 'running' || (next === 'connecting' && wrote))
    if (next !== 'running' && next !== 'connecting') dropHeld()
    view?.setState(next)
    // A shell an inactivity limit ended keeps its last screen while its view shows it (the
    // view never keeps a closed terminal); a sign-out revokes it, which clears the reason.
    if (next === 'closed' && wrote && !transport.endedMessage?.()) { const shown = view; terminal.dispose(); shown?.retired(); return }
    if (next === 'running') {
      fitTerminal()
      // Held keys go last, after the state is published: sending can disconnect the shell
      // (a full input queue), and that newer state must win.
      const keys = held; dropHeld()
      for (const bytes of keys) { if (state !== 'running') break; transport.input(bytes, true) }
    }
  }, message => view?.onError(message))
  const terminal: ShellTerminal = {
    transport,
    get disposed() { return disposed },
    get wrote() { return wrote },
    get state() { return state },
    attach(into, next) {
      if (disposed) return
      root = into; view = next
      into.appendChild(node)
      themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme', 'class', 'style'] })
      media.addEventListener('change', applyTheme)
      observer.observe(into)
      applyTheme()
      next.setState(state)
      fitTerminal()
    },
    detach() {
      if (!root) return
      themeObserver.disconnect(); media.removeEventListener('change', applyTheme); observer.disconnect()
      // No size is sent for a hidden terminal: a resize still scheduled is dropped.
      cancelAnimationFrame(frame); clearTimeout(resizeTimer); resizeTimer = undefined
      node.remove()
      // Keys held during a reconnect are never sent after the person left.
      root = null; view = null; dropHeld()
    },
    dispose() {
      if (disposed) return
      terminal.detach()
      disposed = true
      cancelAnimationFrame(frame); clearTimeout(resizeTimer)
      events.forEach(event => node.removeEventListener(event, activity, true))
      unsubscribe(); input.dispose(); osc52.dispose()
      term.dispose(); node.replaceChildren()
      kept.delete(transport)
    },
    fit: fitTerminal,
    focus() { term.focus() },
  }
  return terminal
}
