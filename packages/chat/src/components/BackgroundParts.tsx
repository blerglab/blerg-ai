// The pieces a host's list of background tasks (the runner's side panel) draws a task with: its
// kind icon, its elapsed clock, its end glyph and its full title.
import { clockLabel, taskFailed, taskOk, type BackgroundTask } from '../model/backgroundTasks'

export const KIND_ICON: Record<string, string> = { bash: '⏱', monitor: '👁', agent: '🤖' }
export const KIND_NAME: Record<string, string> = { bash: 'Background command', monitor: 'Monitor', agent: 'Background agent' }

export function elapsedOf(t: BackgroundTask, now: number): string {
  if (t.startedAt === null) return ''
  const end = t.done?.at ?? now
  return clockLabel(end - t.startedAt)
}

export function StatusGlyph({ t }: { t: BackgroundTask }) {
  if (!t.done) return null
  return taskOk(t)
    ? <span className="bg-glyph bg-ok" role="img" aria-label="finished">✓</span>
    : <span className="bg-glyph bg-bad" role="img" aria-label={taskFailed(t) ? 'failed' : t.done.status || 'ended'}>✕</span>
}

export function titleOf(t: BackgroundTask): string {
  const kind = KIND_NAME[t.kind] ?? 'Background task'
  const end = t.done ? ` — ${t.done.summary || t.done.status}` : ''
  return `${kind}: ${t.description || t.taskId}${end}`
}

export function KindIcon({ t }: { t: BackgroundTask }) {
  return <span className="bg-icon" role="img" aria-label={KIND_NAME[t.kind] ?? 'Background task'}>{KIND_ICON[t.kind] ?? '•'}</span>
}

export const stateClass = (t: BackgroundTask) => (t.done ? (taskOk(t) ? ' done' : ' bad') : '')
