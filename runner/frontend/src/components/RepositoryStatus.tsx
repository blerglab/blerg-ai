import { IconButton } from '@blerglab/chat'
import { useRepositoryStatus } from '../hooks/useRepositoryStatus'
import { repositoryStatusLabel } from '../lib/repositoryStatus'
import './RepositoryStatus.css'

/** Compact status for the full graph tab's header, beside the repository name. */
export default function RepositoryStatus({ sessionId, path, name, active, quiet = false }: { quiet?: boolean; sessionId: string; path: string; name: string; active: boolean }) {
  const snapshot = useRepositoryStatus(sessionId, path, active)
  const label = repositoryStatusLabel(snapshot)
  const { status, error } = snapshot
  const changes = status ? status.changed + status.untracked : 0
  const detail = error || (status?.partial ? `${label}. This read is incomplete; more changes may exist.` : label)
  if (quiet && !error && (!status || !changes && !status.partial)) return null
  return <span className="repository-header-status">
    <span role="img" aria-label={label} title={detail}>{error ? '!' : !status ? '◌' : changes ? '●' : status.partial ? '◌' : '✓'}</span>
    {error && <IconButton label={`Retry state for ${name}`} onClick={snapshot.refresh} />}
  </span>
}
