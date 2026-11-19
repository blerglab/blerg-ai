import ShellDetails from './ShellDetails'
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
/** How long a ready terminal may reconnect before showing an overlay spinner. */
export const QUIET_RECONNECT_MS = 1000
export const SHELL_DISCONNECTED = 'Disconnected.'
export const SHELL_CONTROLLED_ELSEWHERE = 'This shell was opened in another tab with the same browser sign-in.'

export default function ShellView({ name, transport, visible = true, focusVersion = 0, selected = false, onError, keepSession, restoring = false, onNewShell, newShellBusy = false }: { name?: string; onError?: (message: string) => void; transport: ShellTransport; visible?: boolean; focusVersion?: number; selected?: boolean; keepSession?: string; restoring?: boolean; onNewShell?: () => void; newShellBusy?: boolean }) {
  const errorRef = useRef(onError)
  useEffect(() => { errorRef.current = onError }, [onError])
  const root = useRef<HTMLDivElement>(null)
  const terminal = useRef<ShellTerminal | null>(null)
  const fitRef = useRef<(() => void) | null>(null)
  const [gap, setGap] = useState(false)
  const [withdrawing, setWithdrawing] = useState(false)
  const [withdrawn, setWithdrawn] = useState(false)
  const [state, setState] = useState<ShellState>('connecting')
  const [shows, setShows] = useState(false)
  const [readyFor, setReadyFor] = useState<ShellTransport | null>(null)
  const [failedFor, setFailedFor] = useState<ShellTransport | null>(null)
  // A silent shell is ready once connected; later reconnects keep the same terminal. A kept terminal
  // that shows output is ready at once (#281).
  const initial = !shows && readyFor !== transport && failedFor !== transport && (state === 'connecting' || state === 'syncing')
  // A new terminal surface replaces one that displayed output when the shell closes.
  const [surface, setSurface] = useState(0)
  // A ready terminal reconnects quietly: a spinner overlays it only when the reconnect is slow.
  // It never takes space away from xterm. The terminal's own
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
      onOutput: () => { setReadyFor(transport); setShows(true); setGap(transport.outputGap?.() ?? false) },
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
    {state !== 'running' && !(restoring && shows && (state === 'connecting' || state === 'disconnected')) && !(state === 'connecting' && !initial) && <div className={`shell-connection ${state === 'syncing' ? 'shell-sync-header' : ''}`} role="status" style={{ position: 'relative', top: 'auto', bottom: 'auto', left: 'auto', right: 'auto', flexShrink: 0 }}>
      <span>{name && state === 'closed' && <strong className="shell-display-name" title={name}>{name}: </strong>}{state === 'syncing' ? "Syncing the agent's commits…" : state === 'connecting' ? (transport.reconnecting?.() ? 'Reconnecting…' : 'Connecting…') : state === 'closed' ? <EndedLine message={transport.endedMessage?.()} /> : elsewhere ? SHELL_CONTROLLED_ELSEWHERE : SHELL_DISCONNECTED}</span>
      {state === 'disconnected' && <button onClick={() => { void transport.reconnect() }}>Reconnect</button>}
      {state === 'disconnected' && elsewhere && <button onClick={() => { void transport.reconnect(true) }}>Take back control</button>}
      {state === 'closed' && onNewShell && <button disabled={newShellBusy} onClick={onNewShell}>New shell</button>}
      {(state === 'closed' || state === 'disconnected') && <ShellDetails transport={transport} session={keepSession ?? ''} />}
    </div>}
    {transport.info.persistent && !withdrawn && <div className="shell-persistence" role="note">
      <span>Keeps running while disconnected{transport.info.lifetime_until ? ` until ${new Date(transport.info.lifetime_until * 1000).toLocaleString('en-US')}` : ''}.</span>
      {transport.info.runtime === 'host' && transport.info.consent_id && transport.withdrawConsent && <button type="button" className="btn" title="Withdraw consent and end this shell" disabled={withdrawing} onClick={() => {
        setWithdrawing(true)
        void transport.withdrawConsent!().then(() => setWithdrawn(true)).catch(error => errorRef.current?.(error instanceof Error ? error.message : 'Consent could not be withdrawn.')).finally(() => setWithdrawing(false))
      }}>{withdrawing ? 'Withdrawing…' : 'Withdraw consent'}</button>}
    </div>}
    {gap && <div className="shell-output-gap" role="status">Older shell output is no longer available.</div>}
    <div className="shell-output" style={{ position: 'relative', display: 'flex', flexDirection: 'column', flex: 1, minHeight: 0 }}>
    {!initial && state === 'connecting' && slowReconnect && <div className="shell-reconnect" role="status" aria-label="Reconnecting shell" title="Reconnecting shell"><span className="blerg-status-spinner" aria-hidden="true" /></div>}
    {initial && <div className="shell-initial-loader"><InitialLoader loading active={visible} loadKey={transport.info.shell_id} label="shell" shape="terminal" /></div>}
    <div ref={root} className="shell-terminal-renderer" aria-label={name ? `${name} · ${transport.info.runtime} shell in ${transport.info.folder}` : `${transport.info.runtime} shell in ${transport.info.folder}`} style={{ flex: 1, minHeight: 0 }} />
    </div>
  </div>
}

/** "This shell ended." on a line of its own, then why, when known. */
export function EndedLine({ message }: { message?: string }) {
  const text = message || SHELL_ENDED
  const reason = text.startsWith(`${SHELL_ENDED} `) ? text.slice(SHELL_ENDED.length + 1) : ''
  return reason ? <><span>{SHELL_ENDED}</span> <span className="shell-ended-reason">{reason}</span></> : <span>{text}</span>
}
