import { describe, expect, it } from 'vitest'
import { STALE_RUNNING_MS } from './stale'
import { backgroundLabel, backgroundTasks, clockLabel, runningTasks, shownTasks, truncateLabel } from './backgroundTasks'
import type { AgentEvent } from '../types'

const T0 = Date.parse('2026-10-14T12:00:00Z')
let n = 0
function ev(kind: string, payload: unknown, at: number): AgentEvent {
  n++
  return { type: 'agent_event', session_id: 's', client_event_id: `e${n}`, seq: n, ts: new Date(at).toISOString(), kind, payload } as AgentEvent
}
const started = (id: string, at: number, kind = 'bash', description = `task ${id}`) =>
  ev('background_task_started', { task_id: id, call_id: `c-${id}`, kind, description, started_at: new Date(at).toISOString() }, at)
const done = (id: string, at: number, status = 'completed', summary = 'ok') =>
  ev('background_task_done', { task_id: id, call_id: `c-${id}`, kind: 'bash', status, summary }, at)

describe('backgroundTasks', () => {
  it('pairs each start with its end, in the order they started', () => {
    const tasks = backgroundTasks([started('a', T0), started('b', T0 + 1000, 'monitor'), done('a', T0 + 5000)])
    expect(tasks.map(t => [t.taskId, t.kind, t.startedAt, t.done?.status ?? null])).toEqual([
      ['a', 'bash', T0, 'completed'],
      ['b', 'monitor', T0 + 1000, null],
    ])
    expect(runningTasks(tasks, true, T0 + 6000).map(t => t.taskId)).toEqual(['b'])
    // A session that is not live runs nothing.
    expect(runningTasks(tasks, false, T0 + 6000)).toEqual([])
  })

  it('does not count a task with no end that began over 12 hours ago, nor list it', () => {
    const tasks = backgroundTasks([started('old', T0), started('new', T0 + STALE_RUNNING_MS - 3600_000), done('fin', T0)])
    const now = T0 + STALE_RUNNING_MS + 3600_000
    expect(STALE_RUNNING_MS).toBe(12 * 60 * 60 * 1000)
    expect(runningTasks(tasks, true, now).map(t => t.taskId)).toEqual(['new'])
    expect(shownTasks(tasks, now, true, now, new Set()).map(t => t.taskId)).toEqual(['new'])
    // Just inside the threshold it still runs.
    expect(runningTasks(tasks, true, T0 + STALE_RUNNING_MS).map(t => t.taskId)).toEqual(['old', 'new'])
  })

  it('keeps an end whose start is not held, with no start time', () => {
    const [t] = backgroundTasks([ev('background_task_done', { task_id: 'x', call_id: 'c', kind: 'agent', status: 'failed', summary: 's', description: 'Review' }, T0)])
    expect(t).toMatchObject({ taskId: 'x', description: 'Review', startedAt: null, done: { status: 'failed', at: T0 } })
  })

  it('takes a start that arrives after its end, keeping the end', () => {
    const [t] = backgroundTasks([done('a', T0 + 5000), started('a', T0)])
    expect(t).toMatchObject({ taskId: 'a', startedAt: T0, done: { status: 'completed', at: T0 + 5000 } })
  })

  it('is not running when an older page brings the start of a task whose end is held', () => {
    const start = started('a', T0)
    const end = done('a', T0 + 5000)
    // The end is held first (the newest page); the older page puts the start before it (by seq),
    // or, in a store that appends, after it.
    expect(runningTasks(backgroundTasks([end]), true, T0 + 6000)).toEqual([])
    for (const events of [[start, end], [end, start]]) {
      const tasks = backgroundTasks(events)
      expect(tasks).toHaveLength(1)
      expect(runningTasks(tasks, true, T0 + 6000)).toEqual([])
    }
  })

  it('reads one events array once', () => {
    const events = [started('a', T0)]
    expect(backgroundTasks(events)).toBe(backgroundTasks(events))
    expect(backgroundTasks([...events])).not.toBe(backgroundTasks(events))
  })

  it('ignores a payload with no task id, and no events at all is no tasks', () => {
    expect(backgroundTasks([ev('background_task_started', { call_id: 'c' }, T0), ev('background_task_started', null, T0)])).toEqual([])
    expect(backgroundTasks([])).toEqual([])
  })
})

describe('shownTasks', () => {
  const tasks = backgroundTasks([
    started('run', T0), started('ok', T0), started('bad', T0), started('old', T0),
    done('ok', T0 + 60_000), done('bad', T0 + 60_000, 'failed'), done('old', T0 + 1000, 'failed'),
  ])
  const ids = (now: number, opened = T0 + 30_000, dismissed = new Set<string>()) => shownTasks(tasks, now, true, opened, dismissed).map(t => t.taskId)

  it('shows a finished task for 10 s, a failed one until it is dismissed', () => {
    expect(ids(T0 + 65_000)).toEqual(['run', 'ok', 'bad'])
    expect(ids(T0 + 70_000)).toEqual(['run', 'bad'])
    expect(ids(T0 + 70_000, T0 + 30_000, new Set(['bad']))).toEqual(['run'])
  })

  it('drops a finished task that was dismissed at once, even while it lingers', () => {
    expect(ids(T0 + 62_000, T0 + 30_000, new Set(['bad', 'ok']))).toEqual(['run'])
    // A running task is not dismissed.
    expect(ids(T0 + 62_000, T0 + 30_000, new Set(['run']))).toEqual(['run', 'ok', 'bad'])
  })

  it('does not bring back a failure from before the chat was opened', () => {
    // 'old' failed at T0 + 1 s: shown to a chat opened then, not to one opened half a minute on.
    expect(ids(T0 + 70_000, T0)).toEqual(['run', 'bad', 'old'])
    expect(ids(T0 + 70_000, T0 + 30_000)).toEqual(['run', 'bad'])
    expect(ids(T0 + 70_000 + 3_600_000, T0 + 3_600_000)).toEqual(['run'])
  })

  it('shows nothing running in a session that is not live', () => {
    expect(shownTasks(tasks, T0 + 70_000, false, T0 + 3_600_000, new Set()).map(t => t.taskId)).toEqual([])
  })
})

describe('labels', () => {
  it('names one task, counts several', () => {
    expect(backgroundLabel(1, 'Watch the deploy')).toBe('Waiting on a background task: Watch the deploy')
    expect(backgroundLabel(1, '')).toBe('Waiting on a background task')
    expect(backgroundLabel(3, '')).toBe('Waiting on 3 background tasks')
  })

  it('reads elapsed time as mm:ss, h:mm:ss past an hour', () => {
    expect(clockLabel(5_000)).toBe('00:05')
    expect(clockLabel(754_000)).toBe('12:34')
    expect(clockLabel(3_723_000)).toBe('1:02:03')
    expect(clockLabel(-1)).toBe('00:00')
  })

  it('cuts by character, never through an emoji', () => {
    expect(truncateLabel('short', 48)).toBe('short')
    expect(truncateLabel('a'.repeat(47) + '\u{1F600}\u{1F600}', 48)).toBe('a'.repeat(47) + '…')
  })
})
