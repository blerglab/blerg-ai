import { useRef, type ComponentProps } from 'react'
import ShellWorkspace, { type ShellWorkspaceHandle } from '../components/ShellWorkspace'
import { useSessionTabs } from '../hooks/useSessionTabs'
import { useSessionTabCommands } from '../hooks/useSessionTabCommands'
import { useExplorerLayout } from '../hooks/useExplorerLayout'
import SessionTabBar, { ShellToggleButton } from '../components/SessionTabBar'
import SessionGroups from '../components/SessionGroups'
import { createPortal } from 'react-dom'

export default function ShellWorkspaceHarness({ children, ref, ...props }: Omit<ComponentProps<typeof ShellWorkspace>, 'tabs'>) {
  const tabs = useSessionTabs(props.accountId, props.sessionId, window.innerWidth < 768)
  const ownRef = useRef<ShellWorkspaceHandle>(null)
  const shellRef = ref && typeof ref !== 'function' ? ref : ownRef
  useSessionTabCommands(tabs)
  const explorer = useExplorerLayout(false)
  const bar = (group: typeof tabs.groups[number]) => <SessionTabBar tabs={tabs} explorer={explorer} shellOpen={false} group={tabs.phone ? undefined : group} />
  return <div data-session-root style={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
    {/* New shell moved to the session header in the real app (card BLG-450); this harness has no
        header of its own, so it keeps the control available right above the bar it tests. */}
    <ShellToggleButton tabs={tabs} onOpen={() => shellRef.current?.open()} />
    {(tabs.phone || tabs.groups.length === 1) && bar(tabs.groups[0])}
    <ShellWorkspace {...props} ref={shellRef} tabs={tabs}>
      <SessionGroups tabs={tabs} bar={bar} />
      {createPortal(children, tabs.targetFor('chat'))}
    </ShellWorkspace>
  </div>
}
