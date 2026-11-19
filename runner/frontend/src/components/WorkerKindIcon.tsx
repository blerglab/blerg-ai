import { SearchIcon, TicketIcon, LoopIcon, EyeIcon } from './IconButton'
import type { SessionInfo } from '../types'
export default function WorkerKindIcon({ kind }: { kind: SessionInfo['worker_kind'] }) {
 const label = kind === 'card' ? 'Card worker' : kind === 'role' ? 'Role worker' : kind === 'review' ? 'Reviewer' : kind === 'research' ? 'Research' : ''
 return <span data-worker-kind-slot role={label ? 'img' : undefined} aria-label={label || undefined} title={label || undefined} aria-hidden={label ? undefined : true} style={{ display: 'inline-flex', flexShrink: 0, width: 16, height: 16, color: 'var(--fog-dim)' }}>{kind === 'card' ? <TicketIcon /> : kind === 'role' ? <LoopIcon /> : kind === 'review' ? <EyeIcon /> : kind === 'research' ? <SearchIcon /> : null}</span>
}
