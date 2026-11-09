import { useSessionTabs } from '../hooks/useSessionTabs'
import { useIsMobile } from '../hooks/useIsMobile'
import { useExplorerLayout } from '../hooks/useExplorerLayout'
import { signedInAccount } from '../lib/liveBoardCard'
import SessionTabBar from './SessionTabBar'
import SessionGroups from './SessionGroups'
import InsightsTabs from './InsightsTabs'
import { useEffect, useRef } from 'react'
export default function Insights() {
  const tabs = useSessionTabs(signedInAccount(), 'insights-dashboard', useIsMobile())
  const explorer = useExplorerLayout(false)
  const { open, close } = tabs
  const initialized = useRef(false)
  useEffect(() => { if (!initialized.current) { initialized.current = true; open({ kind: 'insights' }, true); close('chat') } }, [open, close])
  const bar = (group: typeof tabs.groups[number], ends = { start: true, end: true }) => <SessionTabBar workspace={false} tabs={tabs} explorer={explorer} group={group} grouped={tabs.groups.length > 1} {...ends} shellOpen={false} />
  return <div className="insights-tab-workspace">
    {(tabs.phone || tabs.groups.length === 1) && bar(tabs.groups[0])}
    <SessionGroups tabs={tabs} bar={bar} />
    <InsightsTabs tabs={tabs} />
  </div>
}

