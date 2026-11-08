import type { ComponentProps } from 'react'
import { sessionRole } from '../lib/sessionRole'
import type { SessionInfo } from '../types'
import ExplorerTreeRow from './ExplorerTreeRow'
import RuntimeIcon from './RuntimeIcon'
import { FileIcon } from '@blerglab/chat'

type Props = Omit<ComponentProps<typeof ExplorerTreeRow>, 'icon' | 'status' | 'children'> & { name: string; items: SessionInfo[]; attention: SessionInfo[]; category?: boolean }
/** Categories and Workers share the same activity, runtime and person-attention summary. */
export default function DockSessionGroup({ name, items, attention: allAttention, category, ...props }: Props) {
  const attention = items.filter(s => allAttention.some(item => item.id === s.id))
  const runtimes = [...new Set(items.map(s => s.runtime).filter(Boolean))]
  const runtimeSummary = runtimes.length > 0
  const runtimeName = runtimes.length > 1 ? 'Mixed runtimes' : runtimes[0] === 'daemon' ? 'Host' : runtimes[0] === 'cluster' ? 'Cluster' : 'Sandbox'
  const titleFor = (s: SessionInfo) => s.title || `Untitled ${sessionRole(s)}`
  const label = `${name}, ${items.length}${runtimeSummary ? `, ${runtimeName}` : ''}${attention.length ? `, ${attention.map(s => `${titleFor(s)}: ${s.status === 'error' ? 'failed' : 'needs you'}`).join(', ')}` : ''}`
  const running = items.filter(s => s.status === 'running').length
  const unread = category && items.some(s => s.unread && sessionRole(s) !== 'worker')
  return <ExplorerTreeRow {...props} aria-label={label} aria-description={`${running} running${unread ? ', unread output' : ''}`} title={label} icon={category ? <FileIcon name={name} directory expanded={props.expanded} /> : null} status={<>
    {running > 0 && !props.expanded && <span className="blerg-status-spinner dock-group-spinner" title={`${running} running`} aria-hidden="true" />}
    <span aria-hidden="true">{attention.some(s => s.status !== 'error') ? ' ?' : ''}{attention.some(s => s.status === 'error') ? ' !' : ''}</span>
    {unread && <span className="dock-channel-unread" role="img" aria-label="Unread category output" title="Unread category output">•</span>}
  </>}><span className="workspace-name">{name} <span className="repository-count">{items.length}</span>{attention.length > 0 && <span className="dock-worker-attention">{attention.map(s => titleFor(s)).join(' · ')}</span>}</span>{runtimeSummary && <span className="dock-runtime-summary">{runtimes.length > 1 ? <span role="img" aria-label="Mixed runtimes" title="Mixed runtimes">◈</span> : <RuntimeIcon runtime={items[0]?.runtime} />}</span>}</ExplorerTreeRow>
}
