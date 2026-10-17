// The background tasks of a session — what its agent left running while it went on (a background
// shell, a Monitor, a background subagent) — read from its background_task_started and
// background_task_done events, and which of them a host's list (the side panel) shows right now. Pure.
import type { AgentEvent, BackgroundTaskDonePayload, BackgroundTaskStartedPayload } from '../types'
import { staleSince } from './stale'
import { parseTs } from './timeLabel'

/** How long a finished task stays listed, with its check or cross, before it goes. */
export const DONE_LINGER_MS = 10_000

export interface BackgroundTask {
  taskId: string
  /** The tool call that started it (the row a jump to it scrolls to). */
  callId: string
  /** bash | monitor | agent (anything else is shown as a task). */
  kind: string
  description: string
  /** The shell command of a bash or monitor task; '' for an agent or a start recorded without it. */
  command: string
  /** Server-time ms it started; null when its start is not held (it began on an older page). */
  startedAt: number | null
  /** Its end, once the engine reported it; at is server-time ms. */
  done: { status: string; summary: string; at: number | null } | null
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function str(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

// The tasks read from each events array: the chat, the side panel and its badge read the same one.
const read = new WeakMap<AgentEvent[], BackgroundTask[]>()

/** backgroundTasks: every task the events start or end, in the order they started. Read once per
 *  events array; treat the result as read-only. */
export function backgroundTasks(events: AgentEvent[]): BackgroundTask[] {
  let tasks = read.get(events)
  if (!tasks) {
    tasks = readTasks(events)
    read.set(events, tasks)
  }
  return tasks
}

function readTasks(events: AgentEvent[]): BackgroundTask[] {
  const byId = new Map<string, BackgroundTask>()
  for (const ev of events) {
    if (ev.kind !== 'background_task_started' && ev.kind !== 'background_task_done') continue
    if (!isRecord(ev.payload)) continue
    const id = str(ev.payload.task_id)
    if (!id) continue
    let t = byId.get(id)
    if (ev.kind === 'background_task_started') {
      const p = ev.payload as unknown as BackgroundTaskStartedPayload
      const fresh: BackgroundTask = {
        taskId: id, callId: str(p.call_id), kind: str(p.kind), description: str(p.description), command: str(p.command),
        startedAt: parseTs(str(p.started_at)) ?? parseTs(ev.ts), done: t?.done ?? null,
      }
      byId.set(id, fresh)
      continue
    }
    const p = ev.payload as unknown as BackgroundTaskDonePayload
    if (!t) {
      t = { taskId: id, callId: str(p.call_id), kind: str(p.kind), description: str(p.description), command: '', startedAt: null, done: null }
      byId.set(id, t)
    }
    t.done = { status: str(p.status), summary: str(p.summary), at: parseTs(ev.ts) }
  }
  return [...byId.values()]
}

/** staleTask: it has no end and began more than STALE_RUNNING_MS before server time now. */
export function staleTask(t: BackgroundTask, now: number): boolean {
  return !t.done && staleSince(t.startedAt, now)
}

/** failed: the task ended badly (its status or its summary says it failed). */
export function taskFailed(t: BackgroundTask): boolean {
  if (!t.done) return false
  // The notice's status is authoritative; the summary is read only when no status came (an old
  // daemon), so a finished command whose description says "failed" is not mistaken for a failure.
  if (t.done.status !== '') return t.done.status === 'failed'
  return /\bfailed\b/i.test(t.done.summary)
}

/** taskOk: the task ended as it should (a completed one, or a monitor that ran its time). */
export function taskOk(t: BackgroundTask): boolean {
  return !!t.done && !taskFailed(t) && (t.done.status === 'completed' || t.done.status === 'expired' || t.done.status === '')
}

/**
 * shownTasks: the tasks a list shows at server time now. Running ones while the session is
 * live (an ended one runs nothing); finished ones for DONE_LINGER_MS after their end; and a failed
 * one until it is dismissed, when it failed since the chat was opened (openedAt, server time), so
 * a failure the reader is not told of elsewhere is not missed and an old one is not brought back.
 * A finished task that was dismissed goes at once. A task with no end that began more than
 * STALE_RUNNING_MS ago is not shown as running.
 */
export function shownTasks(
  tasks: BackgroundTask[], now: number, live: boolean, openedAt: number, dismissed: ReadonlySet<string>,
): BackgroundTask[] {
  return tasks.filter(t => {
    if (!t.done) return live && !staleTask(t, now)
    const at = t.done.at
    if (at === null || dismissed.has(t.taskId)) return false
    if (now - at < DONE_LINGER_MS) return true
    return taskFailed(t) && at >= openedAt - DONE_LINGER_MS
  })
}

/** runningTasks: the tasks still running in a live session at server time now, oldest first (one
 *  with no end that began more than STALE_RUNNING_MS ago is not running). */
export function runningTasks(tasks: BackgroundTask[], live: boolean, now: number): BackgroundTask[] {
  return live ? tasks.filter(t => !t.done && !staleTask(t, now)) : []
}

/** backgroundLabel: the working line's words while background tasks run. */
export function backgroundLabel(count: number, description: string): string {
  if (count === 1) return description ? `Waiting on a background task: ${description}` : 'Waiting on a background task'
  return `Waiting on ${count} background tasks`
}

/** clockLabel: an elapsed time as mm:ss (h:mm:ss past an hour). */
export function clockLabel(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000))
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const ss = String(total % 60).padStart(2, '0')
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${ss}` : `${String(m).padStart(2, '0')}:${ss}`
}

/** truncateLabel: s cut to max characters with an ellipsis. */
export function truncateLabel(s: string, max: number): string {
  const chars = Array.from(s)
  return chars.length > max ? chars.slice(0, max - 1).join('') + '…' : s
}
