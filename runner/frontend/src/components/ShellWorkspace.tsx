import { useCallback, useEffect, useImperativeHandle, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { ShellLease, ShellRateGroup, disconnectOwned, openShell, HostShellConsentRequired, acceptHostShell, type HostShellChallenge, restoreShells as restoreSessionShells, retainedShellsFor, type ShellTransport, SHELL_ENDED, SHELL_NO_LONGER_RUNNING } from '../shellTransport'
import type { SessionTabs, ShellTabView } from '../hooks/useSessionTabs'
import { tabLabel } from '../hooks/useSessionTabs'
import { usePaletteCommands } from '../commands/context'
import ShellView, { EndedLine } from './ShellView'
import InitialLoader from './InitialLoader'
import HostShellConsentDialog from './HostShellConsentDialog'
import { ConfirmPopover } from '@blerglab/chat'
import { ownsAppShortcut } from '../commands/shortcutOwnership'
import './ShellWorkspace.css'

export const SHELL_LIMIT_REASON = 'This session already has four shells. Close a shell tab to open another.'
export const SHELL_UNREACHABLE = 'This shell could not be reached.'
/** A failed inventory is asked again after these waits, then left to Try again. */
const INVENTORY_RETRY_MS = [1000, 2000, 4000, 8000, 10000, 10000, 10000]
export type ShellWorkspaceHandle = { open(): void; toggle(): void; select(id: string): { pending: boolean; shellId?: string } }
type Props = { tabs: SessionTabs; enabled?: boolean; capabilityPending?: boolean; sessionId: string; accountId: string; children: React.ReactNode; ref?: React.Ref<ShellWorkspaceHandle>; createShell?: (session: string, group: ShellRateGroup, lease?: ShellLease) => Promise<ShellTransport>; restoreShells?: (session: string, group: ShellRateGroup, signal?: AbortSignal, existing?: ShellTransport[], closedIds?: string[], lease?: ShellLease) => Promise<ShellTransport[]> }
export default function ShellWorkspace({ tabs, enabled = true, capabilityPending = false, sessionId, children, ref, createShell = openShell, restoreShells = restoreSessionShells }: Props) {
  const { open: openTab, select: selectTab, restoreShellTabs, replaceShellTab, registerShell, closedShellIds, isShellClosed, reconcileClosedShells, setShellCount, targetFor } = tabs
  const tabsRef = useRef(tabs)
  useLayoutEffect(() => { tabsRef.current = tabs })
  const group = useRef<ShellRateGroup>(null as unknown as ShellRateGroup)
  if (!group.current) group.current = new ShellRateGroup()
  // A return draws the shells this page kept for the session at once: each kept terminal's picture
  // (#281), also while the shell capability is still being asked. Their sockets were closed when
  // the view left, so no input reaches them until the inventory below adopts and reconnects them
  // under this view; the reconnect asks the server's replay (#268), whose reset and bounded history
  // replace the picture. They belong to this view from the start (its rate group and a lease of its
  // own), so a Reconnect on one and leaving release it like any shell of this view.
  const [seeded] = useState<ShellTransport[]>(() => (enabled || capabilityPending) && restoreShells === restoreSessionShells
    ? retainedShellsFor(sessionId).filter(p => !isShellClosed(`shell:${p.info.shell_id}`)).map(p => { p.adopt(group.current, new ShellLease()); return p })
    : [])
  const [panes, setPanes] = useState<ShellTransport[]>(seeded)
  const panesRef = useRef<ShellTransport[]>([])
  const ending = useRef(new Map<string, Promise<void>>())
  const listedHandles = useRef(new Set<string>())
  const [restored, setRestored] = useState(false)
  const restoring = useRef<Promise<void>>(Promise.resolve())
  const [busy, setBusy] = useState(false)
  const opening = useRef(false)
  const launching = useRef<Promise<void>>(Promise.resolve())
  const mounted = useRef(true)
  const root = useRef<HTMLDivElement>(null)
  const errorAnchor = useRef<HTMLElement | null>(null)
  const retryError = useRef<() => void>(() => {})
  const [error, setError] = useState('')
  const [errorTitle, setErrorTitle] = useState("Shell didn't start")
  const [consent, setConsent] = useState<HostShellChallenge | null>(null)
  const [consenting, setConsenting] = useState(false)
  // Tabs this page shows without a shell behind them, once restored: the server no longer lists their
  // shell ('ended'), or its inventory could not be read ('unreachable'). Never a blank surface.
  const [gone, setGone] = useState<ReadonlyMap<string, 'ended' | 'unreachable'>>(new Map())
  const [inventoryAttempt, setInventoryAttempt] = useState(0)
  const inventoryRetries = useRef(0)
  // The gone tab a launch replaces, kept across its consent dialog.
  const replacing = useRef<string | undefined>(undefined)
  const anchor = useCallback((shellId?: string) => {
    const scope = root.current?.closest('[data-session-root]') ?? root.current
    const active = document.activeElement
    const tabId = shellId ? `shell:${shellId}` : undefined
    const control = active instanceof HTMLElement && scope?.contains(active) && active.matches(shellId ? 'button' : 'button, textarea:not(.xterm-helper-textarea), input') ? active : null
    const tab = [...scope?.querySelectorAll<HTMLElement>('[data-tab-id]') ?? []].find(node => node.dataset.tabId === tabId)
    // xterm's helper textarea is a one-pixel IME target, never an error anchor.
    errorAnchor.current = (shellId ? tab ?? control : control)
      ?? scope?.querySelector<HTMLElement>('.session-shell-toggle')
      ?? scope?.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]') ?? null
  }, [])
  const shellView = useCallback((transport: ShellTransport, index: number): ShellTabView => {
    const saved = tabsRef.current.tabs.find(t => t.id === `shell:${transport.info.shell_id}`)
    return { kind: 'shell', shellId: transport.info.shell_id, runtime: transport.info.runtime, folder: transport.info.folder, label: saved ? tabLabel(saved.view) : index === 1 ? 'Shell' : `Shell ${index}` }
  }, [])
  const end = useCallback((p: ShellTransport) => {
    if (ending.current.has(p.info.handle)) return ending.current.get(p.info.handle)!
    p.disconnect()
    const attempt = p.end().then(() => { listedHandles.current.delete(p.info.handle); setShellCount(listedHandles.current.size) }).catch(() => {}).finally(() => { ending.current.delete(p.info.handle) })
    ending.current.set(p.info.handle, attempt)
    return attempt
  }, [setShellCount])
  useEffect(() => {
    // Capability is permission to open/refresh, never an authoritative shell list.
    if (!enabled) return
    let active = true
    const abort = new AbortController()
    // This attempt's own claim: the view's rate group outlives it, and a capability
    // refresh can start the next attempt while this one still waits.
    const lease = new ShellLease()
    let retry: ReturnType<typeof setTimeout> | undefined
    // Serialize inventory with launch in both directions: a refresh can arrive
    // while POST/open is still acquiring its first socket.
    restoring.current = (async () => {
      await launching.current
      if (!active) return
      const known = panesRef.current
      try {
        const candidates = await restoreShells(sessionId, group.current, abort.signal, known, closedShellIds(), lease)
        // An abandoned attempt disconnects only what it still holds: a newer attempt of
        // this view, or a returning view, may already have adopted the same transport.
        if (!active) { candidates.filter(p => !known.includes(p)).forEach(p => disconnectOwned(p, lease)); return }
        listedHandles.current = new Set(candidates.map(p => p.info.handle)); setShellCount(listedHandles.current.size)
        reconcileClosedShells(candidates.map(p => `shell:${p.info.shell_id}`))
        const shells = candidates.filter(candidate => {
          if (!isShellClosed(`shell:${candidate.info.shell_id}`)) return true
          void end(candidate); return false
        }).map(candidate => {
          const owned = known.find(p => p.info.handle === candidate.info.handle && p.info.shell_id === candidate.info.shell_id)
          if (owned && owned !== candidate) disconnectOwned(candidate, group.current)
          return owned ?? candidate
        })
        // A shell of this view that ended and says why keeps its last screen and reason.
        const ended = known.filter(p => !shells.includes(p) && !!p.endedMessage?.() && !isShellClosed(`shell:${p.info.shell_id}`))
        shells.push(...ended)
        known.filter(p => !shells.includes(p)).forEach(p => disconnectOwned(p, group.current))
        panesRef.current = shells
        setPanes(shells)
        // A tab whose shell the server no longer lists stays where it is and says the shell ended.
        const live = new Set(shells.map(p => `shell:${p.info.shell_id}`))
        const unlisted = tabsRef.current.tabs.filter(t => t.view.kind === 'shell' && !live.has(t.id) && !isShellClosed(t.id)).map(t => t.id)
        inventoryRetries.current = 0
        setGone(new Map(unlisted.map(id => [id, 'ended'])))
        restoreShellTabs(shells.map((p, i) => shellView(p, i + 1)), unlisted)
      } catch {
        // A transient inventory failure keeps owned terminals; a tab with none says it could not be
        // reached, and the inventory is asked again a few times.
        if (active) {
          const shown = new Set(panesRef.current.map(p => `shell:${p.info.shell_id}`))
          setGone(current => new Map([...current, ...tabsRef.current.tabs.filter(t => t.view.kind === 'shell' && !shown.has(t.id) && current.get(t.id) !== 'ended').map(t => [t.id, 'unreachable'] as const)]))
          const wait = INVENTORY_RETRY_MS[inventoryRetries.current++]
          if (wait !== undefined) retry = setTimeout(() => setInventoryAttempt(n => n + 1), wait)
        }
      }
      finally { if (active) setRestored(true) }
    })()
    return () => { active = false; abort.abort(); clearTimeout(retry) }
  }, [sessionId, restoreShells, enabled, capabilityPending, restoreShellTabs, shellView, closedShellIds, isShellClosed, reconcileClosedShells, setShellCount, end, inventoryAttempt])
  // The capability came back refused: the kept surfaces drawn while it was asked go, released.
  useEffect(() => {
    if (enabled || capabilityPending || !seeded.length) return
    seeded.forEach(p => disconnectOwned(p, group.current))
    panesRef.current = panesRef.current.filter(p => !seeded.includes(p))
    setPanes(current => current.filter(p => !seeded.includes(p)))
  }, [enabled, capabilityPending, seeded])
  useEffect(() => {
    mounted.current = true
    const owner = group.current
    // Every shell this view owns, also a kept one it drew before (or without) a successful inventory.
    // Leaving releases every shell this view owns, a kept one's too: its socket closes (the server's
    // attachment ends, so another tab attaches without taking over), and its queued input and any
    // automatic terminal replies are dropped, never sent after the person left. Only the detached
    // terminal's picture is kept for the return (#281).
    return () => { mounted.current = false; new Set([...panesRef.current, ...seeded]).forEach(p => disconnectOwned(p, owner)) }
  }, [seeded])
  /** Opens a shell in a new tab or, given a gone shell's tab, in that tab's place (the same start path). */
  const open = useCallback(async function openShellTab(replace?: string) {
    if (!enabled || opening.current) return
    anchor()
    opening.current = true; setBusy(true); setError('')
    replacing.current = replace
    let finishLaunch!: () => void
    launching.current = new Promise<void>(resolve => { finishLaunch = resolve })
    retryError.current = () => { void openShellTab(replace) }
    setErrorTitle("Shell didn't start")
    try {
      await restoring.current
      if (!mounted.current) return
      // The shell a replacement takes the place of no longer counts toward the limit.
      const old = replace ? panesRef.current.find(p => `shell:${p.info.shell_id}` === replace) : undefined
      const handles = [...listedHandles.current].filter(h => h !== old?.info.handle)
      if (Math.max(handles.length, panesRef.current.filter(p => p !== old).length, tabsRef.current.tabs.filter(t => t.view.kind === 'shell' && t.id !== replace).length) >= 4) { setError(SHELL_LIMIT_REASON); return }
      const lease = new ShellLease()
      const transport = await createShell(sessionId, group.current, lease)
      // The person closed the tab this shell was to replace while it started: it goes too, also when
      // the view went away meanwhile (its last tabs say so), so it is never kept for a return.
      // Its close intent is recorded first, so a failed DELETE never brings it back: a later inventory
      // that still lists it ends it, as for any closed shell.
      if (replace && !tabsRef.current.tabs.some(t => t.id === replace)) { tabsRef.current.rememberClosedShell(`shell:${transport.info.shell_id}`); void end(transport); return }
      // The view went away while the shell opened; a returning view may have adopted it.
      if (!mounted.current) { disconnectOwned(transport, lease); return }
      replacing.current = undefined
      const labels = new Set(tabsRef.current.tabs.filter(t => t.view.kind === 'shell').map(t => tabLabel(t.view)))
      let number = 1
      while (labels.has(number === 1 ? 'Shell' : `Shell ${number}`)) number++
      const stale = replace ? panesRef.current.find(p => `shell:${p.info.shell_id}` === replace) : undefined
      panesRef.current = [...panesRef.current.filter(p => p !== stale), transport]; setPanes(panesRef.current)
      if (stale) { listedHandles.current.delete(stale.info.handle); void end(stale) }
      listedHandles.current.add(transport.info.handle); setShellCount(listedHandles.current.size)
      if (replace) {
        setGone(current => { const next = new Map(current); next.delete(replace); return next })
        replaceShellTab(replace, shellView(transport, number))
      } else openTab(shellView(transport, number), true)
    } catch (e) {
      if (mounted.current) { if (e instanceof HostShellConsentRequired) setConsent(e.challenge); else setError(e instanceof Error ? e.message : 'Shell unavailable.') }
    } finally { finishLaunch(); opening.current = false; if (mounted.current) setBusy(false) }
  }, [anchor, enabled, createShell, sessionId, openTab, replaceShellTab, shellView, setShellCount, end])
  const focusShell = useCallback(() => {
    const current = panesRef.current.find(p => `shell:${p.info.shell_id}` === tabsRef.current.active) ?? panesRef.current.at(-1)
    if (!current) { void open(); return }
    const id = `shell:${current.info.shell_id}`
    selectTab(id)
  }, [open, selectTab])
  const ready = enabled ? restored : !capabilityPending
  useImperativeHandle(ref, () => ({ open: () => { void open() }, toggle: focusShell, select: id => {
    if (!ready) return { pending: true }
    const pane = enabled ? panesRef.current.find(p => p.info.shell_id === id) ?? panesRef.current[0] : undefined
    if (pane) selectTab(`shell:${pane.info.shell_id}`, false)
    return { pending: false, shellId: pane?.info.shell_id }
  } }), [open, focusShell, ready, selectTab, enabled])
  const commands = useMemo(() => enabled ? [
    { id: 'toggle-shell', title: 'New shell', destructive: true, here: 2, detail: tabs.shellCount >= 4 ? SHELL_LIMIT_REASON : busy ? 'Opening shell…' : undefined, keywords: 'terminal', run: () => { void open() } },
    { id: 'focus-shell', title: 'Focus shell', destructive: true, keywords: 'terminal', shortcut: 'Ctrl / ⌘ `', run: focusShell },
  ] : [], [enabled, tabs.shellCount, busy, open, focusShell])
  usePaletteCommands(commands)
  useEffect(() => {
    const key = (event: KeyboardEvent) => {
      if (enabled && !ownsAppShortcut(event) && (event.ctrlKey || event.metaKey) && !event.altKey && !event.shiftKey && event.code === 'Backquote') {
        event.preventDefault(); event.stopImmediatePropagation(); if (!event.repeat) focusShell()
      }
    }
    window.addEventListener('keydown', key, true)
    return () => window.removeEventListener('keydown', key, true)
  }, [enabled, focusShell])
  useEffect(() => {
    const cleanups = panes.map(p => registerShell(`shell:${p.info.shell_id}`, () => {
      // Closing is local and immediate, including after a network failure. DELETE is
      // best effort; the existing lease/reaper remains responsible for an unreachable shell.
      panesRef.current = panesRef.current.filter(shell => shell !== p); setPanes(panesRef.current)
      void end(p)
    }))
    return () => cleanups.forEach(cleanup => cleanup())
  }, [panes, registerShell, end])
  return <div ref={root} data-shells-restored={ready} className="shell-workspace">
    <div className="shell-chat" inert={!!consent}>{children}</div>
    {!ready && tabs.tabs.filter(tab => tab.view.kind === 'shell' && !panes.some(p => `shell:${p.info.shell_id}` === tab.id)).map(tab => createPortal(
      <InitialLoader loading active={tabs.visible.includes(tab.id)} loadKey={`${sessionId}:${tab.id}`} label="shell" shape="terminal" />, targetFor(tab.id), `loading:${tab.id}`,
    ))}
    {panes.map(p => {
      const id = `shell:${p.info.shell_id}`
      return createPortal(<ShellView transport={p} keepSession={sessionId} restoring={!restored} visible={tabs.visible.includes(id)} selected={tabs.active === id} focusVersion={tabs.shellFocus.id === id ? tabs.shellFocus.version : 0} onNewShell={enabled ? () => { void open(id) } : undefined} newShellBusy={busy} onError={message => {
        // A live failure ended the shell: Retry starts a new one in its place.
        anchor(p.info.shell_id); setErrorTitle('Shell stopped'); retryError.current = () => { void open(id) }; setError(message)
      }} />, targetFor(id), id)
    })}
    {ready && tabs.tabs.filter(tab => tab.view.kind === 'shell' && !panes.some(p => `shell:${p.info.shell_id}` === tab.id)).map(tab => createPortal(
      <div className="shell-terminal shell-gone" data-shell-id={tab.view.kind === 'shell' ? tab.view.shellId : undefined}>
        <div className="shell-connection" role="status">
          <span>{gone.get(tab.id) === 'ended' ? <EndedLine message={`${SHELL_ENDED} ${SHELL_NO_LONGER_RUNNING}`} /> : SHELL_UNREACHABLE}</span>
          {enabled && gone.get(tab.id) !== 'ended' && <button onClick={() => { inventoryRetries.current = 0; setInventoryAttempt(n => n + 1) }}>Try again</button>}
          {enabled && <button disabled={busy} onClick={() => { void open(tab.id) }}>New shell</button>}
        </div>
      </div>, targetFor(tab.id), `gone:${tab.id}`,
    ))}
    {consent && <HostShellConsentDialog challenge={consent} busy={consenting} onCancel={() => setConsent(null)} onAllow={() => {
      setConsenting(true)
      void acceptHostShell(sessionId, consent.challenge_id).then(() => { setConsent(null); void open(replacing.current) }).catch(e => { setError(e instanceof Error ? e.message : 'Consent unavailable.'); setConsent(null) }).finally(() => setConsenting(false))
    }} />}
    {error && <ConfirmPopover placement="anchor" anchor={errorAnchor} title={errorTitle} description={error} danger={false} confirmLabel="Retry" cancelLabel="Dismiss" onConfirm={() => { setError(''); retryError.current() }} onCancel={() => setError('')} testId="shell-error" />}
  </div>
}
