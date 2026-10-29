import { useEffect, useRef, useState } from 'react'
import { SHELL_ENDED, type ShellState, type ShellTransport } from '../shellTransport'
import { keepTerminal, terminalFor, type ShellTerminal } from '../lib/keptTerminals'
import InitialLoader from './InitialLoader'
import '@xterm/xterm/css/xterm.css'

/**
 * keepSession: the session this view belongs to. Given, leaving keeps the terminal (detached,
 * still subscribed) so a return shows it at once (lib/keptTerminals). restoring: the session's
 * shells are still being reconnected; a terminal that already shows output does not say
 * "Connecting" or "Disconnected" meanwhile. onNewShell: a shell that ended offers a new one in its place.
 */
/** How long a terminal that shows output may reconnect before it says "Connecting…". */
export const QUIET_RECONNECT_MS = 1000
export const SHELL_DISCONNECTED = 'Disconnected.'
export const SHELL_CONTROLLED_ELSEWHERE = 'Controlled in another tab.'

export default function ShellView({ transport, visible = true, focusVersion = 0, selected = false, onError, keepSession, restoring = false, onNewShell, newShellBusy = false }: { onError?: (message: string) => void; transport: ShellTransport; visible?: boolean; focusVersion?: number; selected?: boolean; keepSession?: string; restoring?: boolean; onNewShell?: () => void; newShellBusy?: boolean }) {
  const errorRef = useRef(onError)
  useEffect(() => { errorRef.current = onError }, [onError])
  const root = useRef<HTMLDivElement>(null)
  const terminal = useRef<ShellTerminal | null>(null)
  const fitRef = useRef<(() => void) | null>(null)
  const [state, setState] = useState<ShellState>('connecting')
  const [shows, setShows] = useState(false)
  const [readyFor, setReadyFor] = useState<ShellTransport | null>(null)
  const [failedFor, setFailedFor] = useState<ShellTransport | null>(null)
  // A silent shell is ready once connected; later reconnects keep the same terminal. A kept terminal
  // that shows output is ready at once (#281).
  const initial = !shows && readyFor !== transport && failedFor !== transport && (state === 'connecting' || state === 'syncing')
  // A new terminal surface replaces one that displayed output when the shell closes.
  const [surface, setSurface] = useState(0)
  // A terminal that shows its picture reconnects quietly: "Connecting…" appears only when the
  // reconnect is slow, never as a flash above a terminal that looks finished. The terminal's own
  // state reports start and stop the timer (a subscription, not an effect's body).
  const [slowReconnect, setSlowReconnect] = useState(false)
  const slowTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  useEffect(() => {
    if (!root.current) return
    const term = terminalFor(transport)
    terminal.current = term
    fitRef.current = term.fit
    setShows(term.wrote)
    term.attach(root.current, {
      setState: next => {
        setState(next)
        if (next === 'running') setReadyFor(transport)
        clearTimeout(slowTimer.current)
        setSlowReconnect(false)
        if (next === 'connecting') slowTimer.current = setTimeout(() => setSlowReconnect(true), QUIET_RECONNECT_MS)
      },
      onOutput: () => { setReadyFor(transport); setShows(true) },
      onError: message => { setFailedFor(transport); errorRef.current?.(message) },
      retired: () => setSurface(n => n + 1),
    })
    return () => {
      clearTimeout(slowTimer.current)
      terminal.current = null; fitRef.current = null
      term.detach()
      if (keepSession && !term.disposed && term.state !== 'closed') keepTerminal(term, keepSession)
      else term.dispose()
    }
  }, [transport, surface, keepSession])
  useEffect(() => { if (visible) fitRef.current?.() }, [visible])
  const consumedFocus = useRef(0)
  useEffect(() => {
    if (focusVersion && focusVersion !== consumedFocus.current) {
      consumedFocus.current = focusVersion
      if (selected && visible) { fitRef.current?.(); terminal.current?.focus() }
    }
  }, [focusVersion, selected, visible])
  // Take over is offered only when the session's inventory confirmed another tab holds the shell. A
  // disconnect for any other reason offers Reconnect; if another tab turns out to hold it, the
  // reconnect is refused and the inventory then says so.
  const elsewhere = state === 'disconnected' && transport.disconnectedReason?.() === 'elsewhere'
  return <div className="shell-terminal" data-shell-id={transport.info.shell_id} style={{ display: 'flex', flexDirection: 'column', position: 'relative' }}>
    {state !== 'running' && !(restoring && shows && (state === 'connecting' || state === 'disconnected')) && !(shows && state === 'connecting' && !slowReconnect) && <div className={`shell-connection ${state === 'syncing' ? 'shell-sync-header' : ''}`} role="status" style={{ position: 'relative', top: 'auto', bottom: 'auto', left: 'auto', right: 'auto', flexShrink: 0 }}>
      <span>{state === 'syncing' ? "Syncing the agent's commits…" : state === 'connecting' ? (transport.reconnecting?.() ? 'Reconnecting…' : 'Connecting…') : state === 'closed' ? <EndedLine message={transport.endedMessage?.()} /> : elsewhere ? SHELL_CONTROLLED_ELSEWHERE : SHELL_DISCONNECTED}</span>
      {state === 'disconnected' && <button onClick={() => { void transport.reconnect() }}>Reconnect</button>}
      {state === 'disconnected' && elsewhere && <button onClick={() => { void transport.reconnect(true) }}>Take over</button>}
      {state === 'closed' && onNewShell && <button disabled={newShellBusy} onClick={onNewShell}>New shell</button>}
    </div>}
    <div className="shell-output" style={{ position: 'relative', display: 'flex', flexDirection: 'column', flex: 1, minHeight: 0 }}>
    {initial && <div className="shell-initial-loader"><InitialLoader loading active={visible} loadKey={transport.info.shell_id} label="shell" shape="terminal" /></div>}
    <div ref={root} className="shell-terminal-renderer" aria-label={`${transport.info.runtime} shell in ${transport.info.folder}`} style={{ flex: 1, minHeight: 0 }} />
    </div>
  </div>
}

/** "This shell ended." on a line of its own, then why, when known. */
export function EndedLine({ message }: { message?: string }) {
  const text = message || SHELL_ENDED
  const reason = text.startsWith(`${SHELL_ENDED} `) ? text.slice(SHELL_ENDED.length + 1) : ''
  return reason ? <><span>{SHELL_ENDED}</span> <span className="shell-ended-reason">{reason}</span></> : <span>{text}</span>
}
