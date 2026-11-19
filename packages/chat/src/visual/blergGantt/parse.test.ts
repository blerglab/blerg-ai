import { describe, expect, it } from 'vitest'
import { parseGantt } from './parse'

describe('parseGantt', () => {
  it('parses a release plan with sections, durations and milestones', () => {
    const graph = parseGantt('gantt\n title Release plan\n dateFormat YYYY-MM-DD\n axisFormat %m-%d\n section Design\n Plan :done, a, 2025-01-06, 2d\n Build :crit, active, b, after a, 3d\n Ship :milestone, after b, 0d')
    expect(graph).not.toBeNull()
    expect(graph!.title).toBe('Release plan')
    expect(graph!.sections).toEqual(['Design'])
    expect(graph!.tasks).toHaveLength(3)
    const [plan, build, ship] = graph!.tasks
    expect(plan).toMatchObject({ id: 'a', label: 'Plan', done: true, crit: false, active: false, milestone: false })
    expect(plan.start.toISOString().slice(0, 10)).toBe('2025-01-06')
    expect(plan.end.toISOString().slice(0, 10)).toBe('2025-01-08')
    expect(build).toMatchObject({ id: 'b', crit: true, active: true, dependsOn: ['a'] })
    expect(build.start.getTime()).toBe(plan.end.getTime())
    expect(ship).toMatchObject({ milestone: true, dependsOn: ['b'] })
    expect(ship.start.getTime()).toBe(build.end.getTime())
  })

  it('supports an explicit end date instead of a duration', () => {
    const graph = parseGantt('gantt\n section Section\n Completed :done, des1, 2014-01-06,2014-01-08')!
    expect(graph.tasks[0].start.toISOString().slice(0, 10)).toBe('2014-01-06')
    expect(graph.tasks[0].end.toISOString().slice(0, 10)).toBe('2014-01-08')
  })

  it('auto-generates an id when the task line omits one', () => {
    const graph = parseGantt('gantt\n section S\n Task one : 2024-01-01, 2d')!
    expect(graph.tasks[0].id).toBe('task-0')
  })

  it('skips frontmatter and comments', () => {
    const graph = parseGantt('---\ntitle: "Generic diagram"\n---\n%% a comment\ngantt\n section S\n T : 2024-01-01, 1d')
    expect(graph).not.toBeNull()
  })

  it('returns null (falls back) for an unrecognized dateFormat', () => {
    expect(parseGantt('gantt\n dateFormat X\n section S\n T : 0, 1')).toBeNull()
  })

  it('returns null for excludes/todayMarker — outside the declared subset', () => {
    expect(parseGantt('gantt\n excludes weekends\n section S\n T : 2024-01-01, 1d')).toBeNull()
    expect(parseGantt('gantt\n todayMarker off\n section S\n T : 2024-01-01, 1d')).toBeNull()
  })

  it('returns null for a dependency on an id not yet declared', () => {
    expect(parseGantt('gantt\n section S\n T1 : after missing, 1d')).toBeNull()
  })

  it('returns null for a task line with no colon, or an empty token', () => {
    expect(parseGantt('gantt\n section S\n not a task line')).toBeNull()
    expect(parseGantt('gantt\n section S\n T : 2024-01-01,, 1d')).toBeNull()
  })

  it('returns null for an end date before its start date', () => {
    expect(parseGantt('gantt\n section S\n T : 2024-01-05, 2024-01-01')).toBeNull()
  })

  it('returns null for a reused task id', () => {
    expect(parseGantt('gantt\n section S\n A : a, 2024-01-01, 1d\n B : a, 2024-01-02, 1d')).toBeNull()
  })

  it('returns null for anything not a gantt diagram, or with no tasks', () => {
    expect(parseGantt('flowchart LR\nA-->B')).toBeNull()
    expect(parseGantt('gantt\n title Empty')).toBeNull()
  })
})
