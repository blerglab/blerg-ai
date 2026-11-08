import { apiFetch } from '../apiFetch'
import type { TreeStatus } from './workspaceTree'
import { workspaceStatusReason } from './workspaceStatusReason'

export type RepositoryStatusRead =
  | { kind: 'status'; status: TreeStatus; generation?: number; launchGeneration?: string }
  | { kind: 'busy' }
  | { kind: 'failure'; reason: string }

const readFailures: Record<string, string> = {
  daemon_timeout: 'The workspace runtime did not reply.',
  not_running: 'The session has no live workspace.',
  changed: 'The workspace changed during the read. Try Refresh.',
  invalid: 'The runtime returned an invalid workspace reply.',
  forbidden: 'This workspace status read is not permitted.',
  unavailable: 'The workspace reader is unavailable.',
  cancelled: 'The repository state read timed out.',
}

/** Busy is temporary contention, partial is usable data, failure carries a safe reason. */
export async function readRepositoryStatus(sessionId: string, path: string, signal: AbortSignal): Promise<RepositoryStatusRead> {
  const query = new URLSearchParams({ op: 'status', path: path === '.' ? '' : path })
  const response = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/workspace?${query}`, { signal })
  const reply = await response.json().catch(() => null)
  if (response.status === 409 && reply?.code === 'busy' || response.ok && reply?.result?.error_code === 'busy') return { kind: 'busy' }
  if (response.ok && reply?.result?.ok && reply.result.status) return { kind: 'status', status: reply.result.status, generation: reply.result.generation, launchGeneration: reply.result.launch_generation }
  const reason = workspaceStatusReason(reply?.result?.status_reason)
    || (Object.hasOwn(readFailures, reply?.code) ? readFailures[reply.code] : '')
    || (Object.hasOwn(readFailures, reply?.result?.error_code) ? readFailures[reply.result.error_code] : '')
    || 'The repository state could not be read.'
  return { kind: 'failure', reason }
}

export interface RepositoryStatusSnapshot { status: TreeStatus | null; error: string; generation?: number; launchGeneration?: string }
export const emptyRepositoryStatus: RepositoryStatusSnapshot = { status: null, error: '' }

/** Keep the last good state during contention, including an incomplete snapshot. */
export function applyRepositoryStatus(previous: RepositoryStatusSnapshot, next: RepositoryStatusRead): RepositoryStatusSnapshot {
  if (next.kind === 'busy') return previous.error ? { ...previous, error: '' } : previous
  if (next.kind === 'failure') return { ...previous, error: next.reason }
  return { status: next.status, error: '', generation: next.generation, launchGeneration: next.launchGeneration }
}

export function repositoryStatusLabel({ status, error }: RepositoryStatusSnapshot): string {
  if (error) return 'Git status unavailable'
  if (!status) return 'Reading state…'
  const count = status.changed + status.untracked
  if (count) return `${count}${status.partial ? '+' : ''} uncommitted`
  return status.partial ? 'Status incomplete' : 'Clean'
}
