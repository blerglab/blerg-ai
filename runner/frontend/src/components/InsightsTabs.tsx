import { useLocation } from 'react-router-dom'
import { useEffect } from 'react'
import { createPortal } from 'react-dom'
import type { SessionTabs } from '../hooks/useSessionTabs'
import { OPEN_INSIGHTS } from '../lib/openInsights'
import { InsightsDashboard } from './Insights'
import TestPerformance from './TestPerformance'
import {OPEN_TEST_PERFORMANCE} from '../lib/testPerformance'

export default function InsightsTabs({ tabs, workspaceId }: { tabs: SessionTabs; workspaceId?: string }) {
  const location = useLocation()
  const { open } = tabs
  useEffect(() => {
    const receive = (event: Event) => { event.preventDefault(); open({ kind: 'insights', workspace: workspaceId }, true) }
    window.addEventListener(OPEN_INSIGHTS, receive)
    return () => window.removeEventListener(OPEN_INSIGHTS, receive)
  }, [open, workspaceId])
  useEffect(()=>{const receive=(event:Event)=>{const workspace=(event as CustomEvent<{workspace?:string}>).detail?.workspace??workspaceId;open({kind:'test-performance',workspace},true)};window.addEventListener(OPEN_TEST_PERFORMANCE,receive);return()=>window.removeEventListener(OPEN_TEST_PERFORMANCE,receive)},[open,workspaceId])
  return tabs.tabs.map(tab => (tab.view.kind === 'insights' || tab.view.kind === 'test-performance') && createPortal(
    tab.view.kind === 'test-performance' ? <TestPerformance workspaceId={workspaceId ?? tab.view.workspace} active={tabs.visible.includes(tab.id)} /> :
    <InsightsDashboard workspaceId={workspaceId ?? tab.view.workspace} key={location.pathname.startsWith('/insights') ? location.pathname + location.search : 'workspace'} active={tabs.visible.includes(tab.id)} />,
    tabs.targetFor(tab.id), tab.id))
}
