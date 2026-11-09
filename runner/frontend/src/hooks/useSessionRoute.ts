import { useLayoutEffect, useRef } from 'react'
import { useLocation, useNavigate, useNavigationType } from 'react-router-dom'
import type { SessionInfo } from '../types'
import type { SessionTabs } from './useSessionTabs'

/** URLs select conversations; file/tool tabs retain their workspace's URL. */
export function useSessionRoute(tabs: SessionTabs, route: SessionInfo | undefined) {
  const location = useLocation()
  const navigate = useNavigate()
  const navigation = useNavigationType()
  const maxDepth = useRef(0)
  const previous = useRef<{ scope: string; key: string; active: string; depth: number; ids: string[] } | null>(null)
  const { scope, active, sessionId, open, select, takeHistoryReplacement } = tabs
  const conversation = active === 'chat' ? sessionId : tabs.tabs.find(tab => tab.id === active)?.view
  const selected = typeof conversation === 'string' ? conversation : conversation?.kind === 'session-chat' ? conversation.sessionId : undefined
  useLayoutEffect(() => {
    const last = previous.current
    const destination = location.state?.workspaceTab
    // A second tab can open before React Router commits the first navigation.
    // Keep its planned depth until a new location key supplies the browser's depth.
    const depth = last?.scope === scope && last.key === location.key ? last.depth : destination?.scope === scope ? destination.depth ?? 0 : 0
    if (last?.scope !== scope) maxDepth.current = depth
    previous.current = { scope, key: location.key, active, depth, ids: tabs.groups.flatMap(group => group.tabs) }
    if (!route) return
    const validDestination = destination?.scope === scope && (tabs.groups.some(group => group.tabs.includes(destination.id)) || destination.id === '' && active === '')
    if (!last || last.scope !== scope || last.key !== location.key) {
      const direction = last?.scope === scope ? Math.sign(depth - last.depth) : 0
      // Closed/replaced tabs and duplicate destinations do not consume a Back/Forward step.
      if (navigation === 'POP' && destination?.scope === scope && direction && (!validDestination || destination.id === active)) {
        const next = depth + direction
        if (next >= 0 && next <= maxDepth.current) { navigate(direction); return }
      }
      if (validDestination) { previous.current.active = destination.id; if (active !== destination.id) select(destination.id); return }
      if (route.id !== sessionId) {
        if (selected !== route.id) {
          previous.current.active = `session-chat:${route.id}`
          open({ kind: 'session-chat', sessionId: route.id, label: route.title || 'Untitled chat' }, true)
        }
      } else if (last?.scope === scope || selected && selected !== sessionId) { previous.current.active = 'chat'; select('chat') }
      return
    }
    if (last.active !== active) {
      const removed = !previous.current.ids.includes(last.active)
      const replace = takeHistoryReplacement() || removed
      const nextDepth = removed ? depth : depth + 1
      previous.current.depth = nextDepth
      maxDepth.current = replace ? Math.max(maxDepth.current, nextDepth) : nextDepth
      navigate(selected ? `/sessions/${encodeURIComponent(selected)}` : location.pathname, { replace, state: { workspaceTab: { scope, id: active, depth: nextDepth } } })
    }
  }, [scope, active, selected, sessionId, route, location.key, location.pathname, location.state, navigation, tabs.groups, navigate, open, select, takeHistoryReplacement])
}
