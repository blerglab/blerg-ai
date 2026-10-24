import { useCallback, useEffect, useImperativeHandle, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { ShellRateGroup, openShell, HostShellConsentRequired, acceptHostShell, type HostShellChallenge, restoreShells as restoreSessionShells, type ShellTransport } from '../shellTransport'
import ShellView from './ShellView'
import './ShellWorkspace.css'
import { usePaletteCommands } from '../commands/context'
import { useMemo } from 'react'

type Dock = 'bottom' | 'right' | 'maximized' | 'pop-out'
export type ShellWorkspaceHandle = { open(): void }
type Props = { enabled?: boolean; sessionId: string; accountId: string; children: React.ReactNode; ref?: React.Ref<ShellWorkspaceHandle>; createShell?: (session: string, group: ShellRateGroup) => Promise<ShellTransport>; restoreShells?: (session: string, group: ShellRateGroup, signal?: AbortSignal) => Promise<ShellTransport[]> }
function readDock(account: string): Dock {
  try { const d = localStorage.getItem(`shell-dock:${account}`); if (d === 'right' || d === 'maximized' || d === 'pop-out') return d } catch { /* Preferences are optional. */ }
  return 'bottom'
}
export default function ShellWorkspace({ enabled = true, sessionId, accountId, children, ref, createShell = openShell, restoreShells = restoreSessionShells }: Props) {
  const [dock, setDock] = useState<Dock>(() => readDock(accountId))
  const [collapsed, setCollapsed] = useState(false)
  const [panes, setPanes] = useState<ShellTransport[]>([])
  const [active, setActive] = useState('')
  const [split, setSplit] = useState<'right' | 'down'>('right')
  const [error, setError] = useState('')
  const [consent, setConsent] = useState<HostShellChallenge | null>(null)
  const [consenting, setConsenting] = useState(false)
  const [size, setSize] = useState(35)
  const [ratios, setRatios] = useState({ x: 50, y: 50 })
  const [bounds, setBounds] = useState({ width: 0, height: 0 })
  const [focusVersion, setFocusVersion] = useState(0)
  const priorFocus = useRef<HTMLElement | null>(null)
  const hadConsent = useRef(false)
  const [phone, setPhone] = useState(() => window.innerWidth < 768)
  const [surfaceHost] = useState(() => { const node = document.createElement('div'); node.className = 'shell-surface'; node.style.display = 'contents'; return node })
  const surfaceSlot = useRef<HTMLDivElement>(null)
  const [popupRoot, setPopupRoot] = useState<HTMLElement | null>(null)
  const popup = useRef<Window | null>(null)
  const root = useRef<HTMLDivElement>(null)
  const dockBody = useRef<HTMLDivElement>(null)
  const panesRef = useRef<ShellTransport[]>([])
  const group = useRef(new ShellRateGroup())
  const opening = useRef(false)
  const mounted = useRef(true)
  const swipe = useRef<number | null>(null)
  const requestFocus = useCallback(() => {
    const element = document.activeElement
    if (element instanceof HTMLElement && root.current?.querySelector('.shell-chat')?.contains(element)) priorFocus.current = element
    setFocusVersion(n => n + 1)
  }, [])
  const open = useCallback(async () => {
    requestFocus()
    if (!enabled) return
    if (opening.current || panesRef.current.length >= 4) { setCollapsed(false); return }
    opening.current = true; setError('')
    try {
      const transport = await createShell(sessionId, group.current)
      if (!mounted.current) { transport.disconnect(); return }
      panesRef.current = [...panesRef.current, transport]; setPanes(panesRef.current); setActive(transport.info.shell_id); setCollapsed(false)
    } catch (e) { if (mounted.current) { if (e instanceof HostShellConsentRequired) setConsent(e.challenge); else setError(e instanceof Error ? e.message : 'Shell unavailable.') } }
    finally { opening.current = false }
  }, [sessionId, createShell, requestFocus, enabled])
  const commands = useMemo(() => enabled ? [{ id: `shell:${sessionId}`, title: 'Shell', shortcut: 'Ctrl+`', run: () => { void open() } }] : [], [enabled, sessionId, open])
  usePaletteCommands(commands)
  useImperativeHandle(ref, () => ({ open: () => { void open() } }), [open])
  useEffect(() => {
    if (!enabled) return
    let active = true
    const abort = new AbortController()
    void restoreShells(sessionId, group.current, abort.signal).then(restored => {
      if (!active) { restored.forEach(p => p.disconnect()); return }
      const known = new Set(panesRef.current.map(p => p.info.shell_id))
      panesRef.current = [...panesRef.current, ...restored.filter(p => !known.has(p.info.shell_id))].slice(0, 4)
      setPanes(panesRef.current)
      setActive(current => current || panesRef.current[0]?.info.shell_id || '')
    }).catch(() => { /* Unavailable runtimes expose no handles. Open reports the error on demand. */ })
    return () => { active = false; abort.abort() }
  }, [sessionId, accountId, restoreShells, enabled])
  useEffect(() => {
    mounted.current = true
    return () => { mounted.current = false; panesRef.current.forEach(p => p.disconnect()); popup.current?.close() }
  }, [])
  useEffect(() => {
    function key(e: KeyboardEvent) { if (enabled && e.ctrlKey && e.code === 'Backquote') { e.preventDefault(); e.stopPropagation(); if (panesRef.current.length) { requestFocus(); setCollapsed(c => !c) } else void open() } }
    const owners = [window, ...(popup.current ? [popup.current] : [])]
    owners.forEach(win => win.addEventListener('keydown', key, true)); return () => owners.forEach(win => win.removeEventListener('keydown', key, true))
  }, [open, requestFocus, popupRoot, enabled])
  useEffect(() => { const resize = () => setPhone(window.innerWidth < 768); window.addEventListener('resize', resize); return () => window.removeEventListener('resize', resize) }, [])
  useEffect(() => { if (collapsed) priorFocus.current?.focus() }, [collapsed])
  useEffect(() => { if (!consent && hadConsent.current) priorFocus.current?.focus(); hadConsent.current = !!consent }, [consent])
  useLayoutEffect(() => { const target = popupRoot ?? surfaceSlot.current; target?.appendChild(surfaceHost); return () => { surfaceHost.remove() } }, [popupRoot, surfaceHost, panes.length])
  useEffect(() => {
    if (!dockBody.current) return
    const observer = new ResizeObserver(entries => { const b = entries[0].contentRect; setBounds({ width: b.width, height: b.height }) })
    observer.observe(dockBody.current); return () => observer.disconnect()
  }, [panes.length, popupRoot, collapsed])
  async function close(transport: ShellTransport) {
    try { await transport.end(); panesRef.current = panesRef.current.filter(p => p !== transport); setPanes(panesRef.current); setActive(panesRef.current[0]?.info.shell_id ?? '') }
    catch { setError('Shell could not be ended. Reconnect and try again.') }
  }
  function chooseDock(next: Dock) {
    requestFocus()
    if (next === 'pop-out') {
      const win = window.open('', `shell-${crypto.randomUUID()}`, 'popup,width=1000,height=700')
      if (!win) { setError('Allow pop-ups to move the shell to its own window.'); return }
      win.document.title = 'Shell'
      for (const sheet of document.querySelectorAll('style,link[rel="stylesheet"]')) win.document.head.appendChild(sheet.cloneNode(true))
      const node = win.document.createElement('div'); node.className = 'shell-workspace shell-at-pop-out'; node.style.height = '100dvh'; win.document.body.style.margin = '0'; win.document.body.appendChild(node)
      popup.current = win; setPopupRoot(node)
      win.addEventListener('beforeunload', () => { if (mounted.current) { setPopupRoot(null); setDock('bottom') } })
    } else { const old = popup.current; popup.current = null; setPopupRoot(null); old?.close() }
    setDock(next)
    try { localStorage.setItem(`shell-dock:${accountId}`, next) } catch { /* Preferences are optional. */ }
  }
  function drag(event: React.PointerEvent, kind: 'size' | 'x' | 'y') {
    const target = event.currentTarget as HTMLElement; target.setPointerCapture(event.pointerId)
    const rect = (kind === 'size' ? root.current : dockBody.current)?.getBoundingClientRect(); if (!rect) return
    function move(e: PointerEvent) {
      if (kind === 'size') { const n = dock === 'right' ? (rect!.right - e.clientX) / rect!.width : (rect!.bottom - e.clientY) / rect!.height; setSize(Math.min(75, Math.max(20, n * 100))) }
      else { const n = kind === 'x' ? (e.clientX - rect!.left) / rect!.width : (e.clientY - rect!.top) / rect!.height; setRatios(r => ({ ...r, [kind]: Math.min(75, Math.max(25, n * 100)) })) }
    }
    function end() { target.removeEventListener('pointermove', move); target.removeEventListener('pointerup', end); target.removeEventListener('pointercancel', end) }
    target.addEventListener('pointermove', move); target.addEventListener('pointerup', end); target.addEventListener('pointercancel', end)
  }
  const columns = panes.length > 1 && (split === 'right' || panes.length > 2) ? 2 : 1
  const rows = panes.length > 1 && (split === 'down' || panes.length > 2) ? 2 : 1
  const tabs = bounds.width < columns * 300 || bounds.height < rows * 160
  const docked = panes.length > 0
  const surface = <section className={`shell-dock ${collapsed ? 'shell-collapsed' : ''}`} aria-label="Session shells" style={{ '--shell-size': `${size}%` } as React.CSSProperties}>
    {!collapsed && dock !== 'maximized' && dock !== 'pop-out' && <div className="shell-dock-divider" role="separator" aria-label="Resize shell dock" onPointerDown={e => drag(e, 'size')} />}
    <header className="shell-header" onTouchStart={e => { swipe.current = e.touches[0].clientY }} onTouchEnd={e => { if (swipe.current !== null && e.changedTouches[0].clientY - swipe.current > 80) setCollapsed(true); swipe.current = null }}>
      <button aria-label={collapsed ? 'Expand shell' : 'Collapse shell'} onClick={() => { requestFocus(); setCollapsed(c => !c) }}>Shell · running {collapsed ? '▴' : '▾'}</button>
      {!collapsed && <><select aria-label="Dock shell" value={dock} onChange={e => chooseDock(e.target.value as Dock)}><option value="bottom">Bottom</option><option value="right">Right</option><option value="maximized">Maximized</option><option value="pop-out">Pop-out</option></select>
        <button disabled={panes.length >= 4} aria-label="Split right" onClick={() => { setSplit('right'); void open() }}>◫</button>
        <button disabled={panes.length >= 4} aria-label="Split down" onClick={() => { setSplit('down'); void open() }}>⬒</button>
        <button className="shell-return" onClick={() => setCollapsed(true)}>Return to chat</button></>}
    </header>
    <div hidden={collapsed} className="shell-content"><div className="shell-tabs" role="tablist" aria-label="Shell panes">{panes.map(p => <button role="tab" aria-selected={active === p.info.shell_id} key={p.info.shell_id} onClick={() => { requestFocus(); setActive(p.info.shell_id) }}>{p.info.runtime === 'cluster' ? 'pod' : p.info.runtime} · {p.info.folder}</button>)}</div>
    <div ref={dockBody} className={`shell-panes ${tabs ? 'shell-as-tabs' : ''}`} style={{ gridTemplateColumns: columns === 2 ? `${ratios.x}% 1fr` : '1fr', gridTemplateRows: rows === 2 ? `${ratios.y}% 1fr` : '1fr' }}>
      {panes.map(p => <div className="shell-pane" key={p.info.shell_id} data-active={active === p.info.shell_id} onFocusCapture={() => setActive(p.info.shell_id)}>
        <div className="shell-pane-title"><span>{p.info.runtime === 'cluster' ? 'pod' : p.info.runtime} · {p.info.folder}</span><button aria-label={`End shell ${p.info.folder}`} onClick={() => { void close(p) }}>×</button></div>
        <ShellView transport={p} visible={!collapsed && (!tabs || active === p.info.shell_id)} selected={active === p.info.shell_id} focusVersion={focusVersion} />
      </div>)}
      {!tabs && panes.length > 1 && columns === 2 && <div role="separator" aria-label="Resize shell columns" className="shell-split-x" style={{ left: `${ratios.x}%` }} onPointerDown={e => drag(e, 'x')} />}
      {!tabs && panes.length > 1 && rows === 2 && <div role="separator" aria-label="Resize shell rows" className="shell-split-y" style={{ top: `${ratios.y}%` }} onPointerDown={e => drag(e, 'y')} />}
    </div></div>
  </section>
  // A remembered pop-out preference never opens a window without a user gesture.
  const effectiveDock = collapsed && dock !== 'pop-out' ? 'bottom' : dock === 'pop-out' && !popupRoot ? 'bottom' : dock
  return <div ref={root} className={`shell-workspace shell-at-${effectiveDock} ${docked && !collapsed ? 'shell-visible' : ''}`} style={{ '--shell-size': `${size}%` } as React.CSSProperties}>
    <div className="shell-chat" inert={!!consent || docked && !collapsed && (phone || dock === 'maximized')}>{children}</div>
    {consent && <div className="shell-consent" role="dialog" aria-modal="true" aria-labelledby="host-shell-consent-title">
      <h2 id="host-shell-consent-title">Allow a shell on this machine?</h2>
      <p>This runs as the OS account below. Startup files and ordinary account permissions can expose credentials.</p>
      <dl><dt>Machine</dt><dd>{consent.machine_id}</dd><dt>Account</dt><dd>{consent.account} (UID {consent.uid})</dd><dt>Shell</dt><dd>{consent.shell}</dd><dt>Folder</dt><dd>{consent.cwd}</dd></dl>
      <button disabled={consenting} onClick={() => setConsent(null)}>Cancel</button>
      <button autoFocus disabled={consenting} onClick={() => { setConsenting(true); void acceptHostShell(sessionId, consent.challenge_id).then(() => { setConsent(null); void open() }).catch(e => { setError(e instanceof Error ? e.message : 'Consent unavailable.'); setConsent(null) }).finally(() => setConsenting(false)) }}>Allow this launch</button>
    </div>}
    {error && <div className="shell-error" role="alert">{error}<button aria-label="Dismiss shell error" onClick={() => setError('')}>×</button></div>}
    <div ref={surfaceSlot} style={{ display: 'contents' }} />
    {docked && createPortal(surface, surfaceHost)}
  </div>
}
