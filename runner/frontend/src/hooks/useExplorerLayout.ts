import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import { usePaletteCommands } from '../commands/context'
import { useIsMobile } from './useIsMobile'
import { signedInAccount } from '../lib/liveBoardCard'

type Preference = { open: boolean; width: number }
export const explorerPrefKey = (account: string) => `blerg-runner.explorer.${encodeURIComponent(account)}`
export const EXPLORER_CHAT_MIN = 320
export function explorerBounds(viewport: number, room = viewport) {
  return { min: Math.min(viewport * .25, Math.max(220, viewport * .08)), max: Math.max(Math.min(viewport * .25, Math.max(220, viewport * .08)), Math.min(viewport * .4, room - EXPLORER_CHAT_MIN)), default: Math.max(280, viewport * .2) }
}
export function explorerWidth(width: number, viewport: number, room = viewport) {
  const bounds = explorerBounds(viewport, room)
  return Math.round(Math.max(bounds.min, Math.min(bounds.max, width)))
}
function readPreference(account: string): Preference {
  try {
    const value = JSON.parse(localStorage.getItem(explorerPrefKey(account)) ?? 'null')
    return { open: value?.open === true, width: typeof value?.width === 'number' && Number.isFinite(value.width) ? value.width : explorerBounds(window.innerWidth).default }
  } catch { return { open: false, width: explorerBounds(window.innerWidth).default } }
}

export function useExplorerLayout(enabled: boolean) {
  const account = signedInAccount()
  const [held, setHeld] = useState(() => ({ account, preference: readPreference(account) }))
  // Sign-in changes never inherit another person's layout, including before effects run.
  if (held.account !== account) setHeld({ account, preference: readPreference(account) })
  const preference = held.account === account ? held.preference : readPreference(account)
  const [viewport, setViewport] = useState(() => window.innerWidth)
  const mobile = useIsMobile()
  const columnsRef = useRef<HTMLDivElement>(null)
  const [room, setRoom] = useState<number | null>(null)
  useEffect(() => {
    const columns = columnsRef.current
    if (!columns) return
    const measure = () => {
      const reserved = [...columns.children].filter(child => !child.matches('.explorer-dock, [data-testid="session-chat-column"]')).reduce((sum, child) => sum + child.getBoundingClientRect().width, 0)
      setRoom(columns.clientWidth - reserved)
    }
    const observer = new ResizeObserver(measure)
    const observe = () => { observer.disconnect(); observer.observe(columns); for (const child of columns.children) observer.observe(child); measure() }
    const children = new MutationObserver(observe)
    children.observe(columns, { childList: true })
    observe()
    return () => { observer.disconnect(); children.disconnect() }
  }, [enabled])
  const triggerRef = useRef<HTMLButtonElement>(null)
  const id = useId()
  useEffect(() => {
    const resize = () => setViewport(window.innerWidth)
    window.addEventListener('resize', resize)
    return () => window.removeEventListener('resize', resize)
  }, [])
  const update = useCallback((change: Partial<Preference>) => {
    setHeld(old => {
      const next = { ...(old.account === account ? old.preference : readPreference(account)), ...change }
      try { localStorage.setItem(explorerPrefKey(account), JSON.stringify(next)) } catch { /* Storage may be disabled. */ }
      return { account, preference: next }
    })
  }, [account])
  const toggle = useCallback(() => update({ open: !preference.open }), [update, preference.open])
  const close = useCallback(() => { update({ open: false }); triggerRef.current?.focus() }, [update])
  const commands = useMemo(() => enabled ? [{ id: 'toggle-explorer', title: 'Toggle explorer', run: toggle }] : [], [enabled, toggle])
  usePaletteCommands(commands)
  return { account, id, triggerRef, columnsRef, open: enabled && preference.open, mobile, width: explorerWidth(preference.width, viewport, room ?? viewport), bounds: explorerBounds(viewport, room ?? viewport), toggle, close, resize: (width: number) => update({ width: explorerWidth(width, viewport, room ?? viewport) }) }
}
export type ExplorerLayout = ReturnType<typeof useExplorerLayout>
