import { useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import type { ShellState, ShellTransport } from '../shellTransport'
import '@xterm/xterm/css/xterm.css'

export default function ShellView({ transport, visible = true, focusVersion = 0, selected = false }: { transport: ShellTransport; visible?: boolean; focusVersion?: number; selected?: boolean }) {
  const root = useRef<HTMLDivElement>(null)
  const terminal = useRef<Terminal | null>(null)
  const fitRef = useRef<(() => void) | null>(null)
  const [state, setState] = useState<ShellState>('connecting')
  useEffect(() => {
    if (!root.current) return
    const term = new Terminal({ scrollback: 2000, screenReaderMode: true, fontSize: 13, fontFamily: 'monospace', convertEol: false, disableStdin: true, theme: { background: '#171717', foreground: '#eeeeee' } })
    terminal.current = term
    const fit = new FitAddon(); term.loadAddon(fit); term.open(root.current)
    // Do not permit OSC clipboard, window operations, or titles to escape the shell surface.
    const osc52 = term.parser.registerOscHandler(52, () => true)
    const encoder = new TextEncoder()
    // xterm also emits automatic terminal-query replies through onData. Only DOM human
    // input events can mark bytes as activity; output parsing has no such authority.
    let humanEvent: Event | undefined
    const activity = (event: Event) => { humanEvent = event; transport.activity?.() }
    const events = ['keydown', 'keypress', 'beforeinput', 'paste', 'compositionstart', 'compositionupdate', 'compositionend', 'input']
    const node = root.current
    events.forEach(event => node.addEventListener(event, activity, true))
    const input = term.onData(data => transport.input(encoder.encode(data), !!humanEvent && humanEvent.eventPhase !== Event.NONE))
    let pending = 0
    const unsubscribe = transport.subscribe(bytes => {
      if (pending + bytes.byteLength > 262144) { transport.disconnect(); return }
      pending += bytes.byteLength
      term.write(bytes, () => { pending -= bytes.byteLength })
    }, next => { term.options.disableStdin = next !== 'running'; setState(next); if (next === 'closed') term.reset(); if (next === 'running') fitTerminal() })
    function fitTerminal() {
      if (!root.current || root.current.clientWidth < 20 || root.current.clientHeight < 20) return
      fit.fit(); transport.resize(term.cols, term.rows)
    }
    fitRef.current = fitTerminal
    const observer = new ResizeObserver(fitTerminal); observer.observe(root.current)
    fitTerminal()
    return () => { terminal.current = null; events.forEach(event => node.removeEventListener(event, activity, true)); fitRef.current = null; observer.disconnect(); unsubscribe(); input.dispose(); osc52.dispose(); term.dispose() }
  }, [transport])
  useEffect(() => { if (visible) fitRef.current?.() }, [visible])
  useEffect(() => { if (focusVersion && selected && visible) { fitRef.current?.(); terminal.current?.focus() } }, [focusVersion, selected, visible])
  return <div className="shell-terminal" data-shell-id={transport.info.shell_id}>
    <div ref={root} className="shell-terminal-renderer" aria-label={`${transport.info.runtime} shell in ${transport.info.folder}`} />
    {state !== 'running' && <div className={`shell-connection ${state === 'syncing' ? 'shell-sync-header' : ''}`} role="status">
      <span>{state === 'syncing' ? "Syncing the agent's commits…" : state === 'connecting' ? 'Connecting…'  : state === 'closed' ? 'Shell ended' : 'Disconnected or controlled in another tab'}</span>
      {state === 'disconnected' && <><button onClick={() => { void transport.reconnect() }}>Reconnect</button><button onClick={() => { void transport.reconnect(true) }}>Take over</button></>}
    </div>}
  </div>
}
