import { useCallback, useEffect, useRef, useState } from 'react'
import { useIsMobile } from './useIsMobile'
import { panePrefKey, usePaneLayout, useViewportWidth } from './usePaneLayout'

export const explorerPrefKey = (account: string) => panePrefKey('explorer', account)
export const EXPLORER_CHAT_MIN = 320
export function explorerBounds(viewport: number, room = viewport) {
  return { min: Math.min(viewport * .25, Math.max(220, viewport * .08)), max: Math.max(Math.min(viewport * .25, Math.max(220, viewport * .08)), Math.min(viewport * .4, room - EXPLORER_CHAT_MIN)), default: Math.max(280, viewport * .2) }
}
export function explorerWidth(width: number, viewport: number, room = viewport) {
  const bounds = explorerBounds(viewport, room)
  return Math.round(Math.max(bounds.min, Math.min(bounds.max, width)))
}
export function useExplorerLayout(enabled: boolean, sessionId = '') {
  const viewport = useViewportWidth()
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
  const pane = usePaneLayout('explorer', 'Toggle explorer', explorerBounds(viewport, room ?? viewport), false, enabled, sessionId)
  const mobileTriggerRef = useRef<HTMLButtonElement>(null)
  const closePane = pane.close
  const close = useCallback(() => { if (mobile && history.state?.blergSheet === 'explorer') history.back(); closePane(); if (mobile) mobileTriggerRef.current?.focus() }, [mobile, closePane])
  return { ...pane, columnsRef, mobileTriggerRef, mobile, triggerRef: mobile ? mobileTriggerRef : pane.triggerRef, close }
}
export type ExplorerLayout = ReturnType<typeof useExplorerLayout>
