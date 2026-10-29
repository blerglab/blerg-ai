import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { PaneContext, SessionToolsHost, type PaneHandle } from '../panes/context'
import { usePaletteCommands } from '../commands/context'
import { tabFocusShortcut, visibleSurfaceIsOpen } from '../commands/shortcutOwnership'
import { signedInAccount } from '../lib/liveBoardCard'

export default function PaneProvider({ children, scope = '' }: { children: ReactNode; scope?: string }) {
  const [host, setHost] = useState<HTMLElement | null>(null)
  const tools = useMemo(() => ({ host, setHost }), [host])
  const [focused, setFocused] = useState(false)
  const account = signedInAccount()
  const identity = JSON.stringify([account, scope])
  const [owner, setOwner] = useState(identity)
  if (owner !== identity) { setOwner(identity); setFocused(false) }
  const active = owner === identity && focused
  const previousFocus = useRef({ active: false, identity })
  const [announced, setAnnounced] = useState('')
  useEffect(() => {
    const previous = previousFocus.current
    previousFocus.current = { active, identity }
    if (previous.active === active) return
    const mode = scope.startsWith('/sessions/') ? 'tab focus mode' : 'focus mode'
    setAnnounced(`${active ? 'Entered' : 'Exited'} ${mode}`)
    // Navigation has its own focus policy, including retained session surfaces.
    if (previous.identity !== identity) return
    const group = [...document.querySelectorAll<HTMLElement>('.session-group')].find(node => node.getClientRects().length && (node.classList.contains('focused') || node.dataset.groupId === 'phone'))
    group?.querySelector<HTMLElement>('.session-view-target:not([hidden]) .xterm-helper-textarea, .session-view-target:not([hidden]) .hosted-view:not([hidden]) [role="region"], .session-view-target:not([hidden]) .agent-composer textarea, .session-view-target:not([hidden]) [data-palette-shortcuts="ignore"] textarea')?.focus({ preventScroll: true })
  }, [active, identity, scope])
  const handles = useRef(new Map<string, { current: PaneHandle }>())
  const snapshot = useRef<Map<string, { handle: { current: PaneHandle }; open: boolean }> | null>(null)
  // Focus is temporary; navigation restores the normal pane preferences.
  useEffect(() => { snapshot.current = null }, [identity])
  const register = useCallback((id: string, handle: { current: PaneHandle }) => {
    handles.current.set(id, handle)
    return () => {
      if (handles.current.get(id) === handle) {
        handles.current.delete(id)
        snapshot.current?.delete(id)
      }
    }
  }, [])
  const restore = useCallback(() => {
    if (!snapshot.current) return
    const previous = snapshot.current
    snapshot.current = null
    for (const [id, { handle, open }] of previous) {
      if (handles.current.get(id) === handle) handle.current.restoreOpen(open)
    }
    setFocused(false)
  }, [])
  const toggle = useCallback(() => {
    if (snapshot.current) restore()
    else {
      snapshot.current = new Map([...handles.current].map(([id, handle]) => [id, { handle, open: handle.current.open }]))
      setFocused(true)
    }
  }, [restore])
  const commands = useMemo(() => [{ id: 'focus-mode', destructive: false, title: 'Focus mode', keywords: 'Focus this tab maximize restore layout', shortcut: 'Ctrl/⌘+Shift+Enter', run: toggle }], [toggle])
  usePaletteCommands(commands)
  useEffect(() => {
    if (!scope.startsWith('/sessions/')) return
    const keydown = (event: KeyboardEvent) => {
      if (!tabFocusShortcut(event) || event.defaultPrevented || event.isComposing || event.keyCode === 229) return
      // Dialogs and review comment forms retain their own Enter bindings.
      if (visibleSurfaceIsOpen('[aria-modal="true"], [role="dialog"], [role="alertdialog"], dialog[open], [role="menu"], .confirm-popover')) return
      if ([...event.composedPath(), document.activeElement].some(target => target instanceof Element && target.closest('.review-form'))) return
      // Reserve this binding before a terminal or composer can send Enter.
      event.preventDefault(); event.stopImmediatePropagation()
      if (!event.repeat) toggle()
    }
    window.addEventListener('keydown', keydown, true)
    return () => window.removeEventListener('keydown', keydown, true)
  }, [scope, toggle])
  const value = useMemo(() => ({ focused: active, register, restore, toggle }), [active, register, restore, toggle])
  return <PaneContext.Provider value={value}><SessionToolsHost.Provider value={tools}>{children}<span className="session-groups-announce" aria-live="polite" data-testid="tab-focus-announce">{announced}</span></SessionToolsHost.Provider></PaneContext.Provider>
}
