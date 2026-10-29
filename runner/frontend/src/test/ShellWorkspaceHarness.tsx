import { useRef, type ComponentProps } from 'react'
import ShellWorkspace, { type ShellWorkspaceHandle } from '../components/ShellWorkspace'
import { useSessionTabs } from '../hooks/useSessionTabs'
import { useSessionTabCommands } from '../hooks/useSessionTabCommands'
import { useExplorerLayout } from '../hooks/useExplorerLayout'
import SessionTabBar from '../components/SessionTabBar'
import SessionGroups from '../components/SessionGroups'
import { createPortal } from 'react-dom'

export default function ShellWorkspaceHarness({ children, ref, ...props }: Omit<ComponentProps<typeof ShellWorkspace>, 'tabs'>) {
  const tabs = useSessionTabs(props.accountId, props.sessionId, window.innerWidth < 768)
  const ownRef = useRef<ShellWorkspaceHandle>(null)
  const shellRef = ref && typeof ref !== 'function' ? ref : ownRef
  useSessionTabCommands(tabs)
  const explorer = useExplorerLayout(false)
  const bar = (group: typeof tabs.groups[number]) => <SessionTabBar tabs={tabs} explorer={explorer} shell={() => shellRef.current?.open()} shellOpen={false} group={tabs.phone ? undefined : group} />
  return <div data-session-root style={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
    {(tabs.phone || tabs.groups.length === 1) && bar(tabs.groups[0])}
    <ShellWorkspace {...props} ref={shellRef} tabs={tabs}>
      <SessionGroups tabs={tabs} bar={bar} />
      {createPortal(children, tabs.targetFor('chat'))}
    </ShellWorkspace>
  </div>
}
