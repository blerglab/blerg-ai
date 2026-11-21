import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { signedInAccount } from '../lib/liveBoardCard'
import { usePaneControl } from '../panes/context'

export type PaneBounds = { min: number; max: number; default: number }
type Preference = { open: boolean; width: number }
export const panePrefKey = (pane: string, account: string) => `blerg-runner.${pane}.${encodeURIComponent(account)}`
export const paneWidth = (width: number, bounds: PaneBounds) => Math.round(Math.max(bounds.min, Math.min(bounds.max, width)))
export function useViewportWidth() {
  const [width, setWidth] = useState(() => window.innerWidth)
  useEffect(() => { const resize = () => setWidth(window.innerWidth); window.addEventListener('resize', resize); return () => window.removeEventListener('resize', resize) }, [])
  return width
}
export function usePaneLayout(pane: string, title: string, bounds: PaneBounds, defaultOpen = false, enabled = true, scope = '') {
  const account = signedInAccount()
  const key = panePrefKey(pane, account) + (scope ? `.${encodeURIComponent(scope)}` : '')
  function read(): Preference {
    try {
      const value = JSON.parse(localStorage.getItem(key) ?? localStorage.getItem(panePrefKey(pane, account)) ?? 'null')
      return { open: typeof value?.open === 'boolean' ? value.open : defaultOpen, width: typeof value?.width === 'number' && Number.isFinite(value.width) ? value.width : bounds.default }
    } catch { console.warn('Pane preference unavailable: invalid browser storage'); return { open: defaultOpen, width: bounds.default } }
  }
  const [held, setHeld] = useState(() => ({ key, preference: read() }))
  if (held.key !== key) setHeld({ key, preference: read() })
  const preference = held.key === key ? held.preference : read()
  const update = useCallback((change: Partial<Preference>) => {
    setHeld(old => {
      // A callback belongs to its rendered account; never write another account's state.
      const next = { ...old.preference, ...change }
      if (old.key !== key) return old
      try { localStorage.setItem(key, JSON.stringify(next)) } catch { console.warn('Pane preference save failed: browser storage unavailable') }
      return { key, preference: next }
    })
  }, [key])
  const setOpen = useCallback((open: boolean) => update({ open }), [update])
  useEffect(() => {
    const capture = (event: Event) => { const detail = (event as CustomEvent).detail; if (detail.scope === scope) detail.panels[pane] = preference }
    const apply = (event: Event) => { const detail = (event as CustomEvent).detail; const value = detail.panels?.[pane]; if (detail.scope === scope && value && typeof value.open === 'boolean' && Number.isFinite(value.width)) update({ open: value.open, width: paneWidth(value.width, bounds) }) }
    window.addEventListener('blerg-layout-capture', capture)
    window.addEventListener('blerg-layout-apply', apply)
    return () => { window.removeEventListener('blerg-layout-capture', capture); window.removeEventListener('blerg-layout-apply', apply) }
  }, [scope, pane, preference, update, bounds])
  const control = usePaneControl(pane, title, { destructive: false }, enabled && preference.open, setOpen, enabled, scope)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const id = useId()
  const changeOpen = control.setOpen
  const close = useCallback(() => { changeOpen(false); triggerRef.current?.focus() }, [changeOpen])
  return { account, id, triggerRef, open: enabled && control.open, toggle: control.toggle, close, bounds, width: paneWidth(preference.width, bounds), resize: (width: number) => update({ width: paneWidth(width, bounds) }), reset: () => update({ width: paneWidth(bounds.default, bounds) }) }
}
/** Size and disclosure identity without registering another visibility controller. */
export function usePaneDimensions(pane: string, bounds: PaneBounds, account = signedInAccount(), scope = '') {
  const key = panePrefKey(pane, account) + '.size' + (scope ? `.${encodeURIComponent(scope)}` : '')
  const read = () => {
    try { const n = Number(localStorage.getItem(key) ?? localStorage.getItem(panePrefKey(pane, account) + '.size')); return n > 0 && Number.isFinite(n) ? n : null } catch { return null }
  }
  const [held, setHeld] = useState(() => ({ key, width: read() }))
  if (held.key !== key) setHeld({ key, width: read() })
  const resize = (width: number) => {
    const next = paneWidth(width, bounds)
    try { localStorage.setItem(key, String(next)) } catch { console.warn('Pane width save failed: browser storage unavailable') }
    setHeld({ key, width: next })
  }
  const triggerRef = useRef<HTMLButtonElement>(null)
  const id = useId()
  return { account, id, triggerRef, bounds, width: paneWidth((held.key === key ? held.width : read()) ?? bounds.default, bounds), resize, reset: () => resize(bounds.default) }
}

export type PaneLayout = ReturnType<typeof usePaneLayout>
