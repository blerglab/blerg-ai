import { describe, expect, it } from 'vitest'
import type { AgentEvent, SessionInfo, StartStagePayload } from '../types'
import { latestStartAttempt, placeholderAttempt, startPanelVisible, withSessionOutcome } from './startStages'

let n = 0
function stageEv(payload: StartStagePayload, ts = '2026-09-26T10:00:00Z'): AgentEvent {
  n++
  return { type: 'agent_event', session_id: 's1', client_event_id: `e${n}`, seq: n, ts, kind: 'start_stage', payload }
}

const clusterPlan: StartStagePayload = {
  plan: true,
  runtime: 'cluster',
  stages: [
    { id: 'queued', label: 'Queued', state: 'active' },
    { id: 'schedule', label: 'Scheduling pod', state: 'pending' },
    { id: 'image', label: 'Pulling image', state: 'pending' },
    { id: 'connect', label: 'Connecting to server', state: 'pending' },
    { id: 'clone', label: 'Cloning repo', state: 'pending' },
    { id: 'engine', label: 'Starting agent', state: 'pending' },
    { id: 'ready', label: 'Ready', state: 'pending' },
  ],
}

const session: SessionInfo = {
  id: 's1', daemon_id: 'd', status: 'starting', project_path: '/w', repo: 'org/r', title: 't',
  started_at: '2026-09-26T10:00:00Z', kind: 'agent', runtime: 'cluster',
}

function states(events: AgentEvent[]) {
  return Object.fromEntries(latestStartAttempt(events)!.stages.map(s => [s.id, s.state]))
}

describe('latestStartAttempt', () => {
  it('returns null without start events', () => {
    expect(latestStartAttempt([])).toBeNull()
  })

  it('folds updates into the plan and marks earlier stages done', () => {
    const evs = [
      stageEv(clusterPlan),
      stageEv({ stages: [{ id: 'image', state: 'active', detail: 'Pulling the image' }] }, '2026-09-26T10:00:20Z'),
    ]
    expect(states(evs)).toMatchObject({ queued: 'done', schedule: 'done', image: 'active', clone: 'pending' })
    const a = latestStartAttempt(evs)!
    expect(a.runtime).toBe('cluster')
    expect(a.stages.find(s => s.id === 'image')!.detail).toBe('Pulling the image')
    expect(a.ready).toBe(false)
  })

  it('keeps the failure reason and hint of a failed stage', () => {
    const evs = [
      stageEv(clusterPlan),
      stageEv({ stages: [{ id: 'image', state: 'failed', detail: "Can't pull (ImagePullBackOff)", hint: 'Check the image' }] }),
    ]
    const a = latestStartAttempt(evs)!
    expect(a.failed).toBe(true)
    expect(a.stages.find(s => s.id === 'image')).toMatchObject({ state: 'failed', hint: 'Check the image' })
  })

  it('a failed stage that recovers is done once a later stage moves', () => {
    const evs = [
      stageEv(clusterPlan),
      stageEv({ stages: [{ id: 'schedule', state: 'failed', detail: 'no node' }] }),
      stageEv({ stages: [{ id: 'image', state: 'active' }] }),
    ]
    expect(states(evs)).toMatchObject({ schedule: 'done', image: 'active' })
    expect(latestStartAttempt(evs)!.failed).toBe(false)
  })

  it('ready closes the attempt', () => {
    const evs = [stageEv(clusterPlan), stageEv({ stages: [{ id: 'ready', state: 'done' }] }, '2026-09-26T10:01:30Z')]
    const a = latestStartAttempt(evs)!
    expect(a.ready).toBe(true)
    expect(a.readyAt).toBe(Date.parse('2026-09-26T10:01:30Z'))
    expect(a.stages.every(s => s.state === 'done')).toBe(true)
  })

  it('a new plan (a resume) starts a fresh attempt', () => {
    const evs = [
      stageEv(clusterPlan),
      stageEv({ stages: [{ id: 'ready', state: 'done' }] }),
      stageEv({ ...clusterPlan, stages: clusterPlan.stages.map((s, i) => i === 0 ? { ...s, detail: 'Resuming' } : s) }),
    ]
    const a = latestStartAttempt(evs)!
    expect(a.ready).toBe(false)
    expect(a.stages[0]).toMatchObject({ state: 'active', detail: 'Resuming' })
  })
})

describe('startPanelVisible / withSessionOutcome', () => {
  const evs = [stageEv(clusterPlan), stageEv({ stages: [{ id: 'clone', state: 'active' }] })]

  it('shows while starting, even before any stage arrived', () => {
    expect(startPanelVisible(session, null)).toBe(true)
  })

  it('hides once the session is live', () => {
    expect(startPanelVisible({ ...session, status: 'idle' }, latestStartAttempt(evs))).toBe(false)
  })

  it('keeps a start that failed on screen with the session error as its reason', () => {
    const errored = { ...session, status: 'error' as const, error_reason: 'Git authentication failed' }
    const attempt = withSessionOutcome(errored, latestStartAttempt(evs))!
    expect(startPanelVisible(errored, attempt)).toBe(true)
    expect(attempt.failed).toBe(true)
    expect(attempt.stages.find(s => s.id === 'clone')).toMatchObject({ state: 'failed', detail: 'Git authentication failed' })
  })

  it('shows a resume in progress on a disconnected session, not a finished one', () => {
    const disc = { ...session, status: 'disconnected' as const }
    expect(startPanelVisible(disc, latestStartAttempt(evs))).toBe(true)
    const done = [...evs, stageEv({ stages: [{ id: 'ready', state: 'done' }] })]
    expect(startPanelVisible(disc, latestStartAttempt(done))).toBe(false)
    expect(startPanelVisible(disc, null)).toBe(false)
  })

  it('placeholder has an active first stage', () => {
    expect(placeholderAttempt(1).stages[0].state).toBe('active')
  })
})

describe('plugins stage (data-driven, no special casing)', () => {
  const withPlugins: StartStagePayload = {
    ...clusterPlan,
    stages: [
      ...clusterPlan.stages!.slice(0, 5),
      { id: 'plugins', label: 'Installing plugins', state: 'pending', detail: 'frontend-design, superpowers' },
      ...clusterPlan.stages!.slice(5),
    ],
  }

  it('keeps the planned label and position, and folds the pod reports into it', () => {
    const evs = [
      stageEv(withPlugins),
      stageEv({ stages: [{ id: 'clone', state: 'done' }, { id: 'plugins', state: 'active', detail: 'Installing 2 plugin(s)' }] }),
    ]
    expect(latestStartAttempt(evs)!.stages.map(s => s.id)).toEqual(
      ['queued', 'schedule', 'image', 'connect', 'clone', 'plugins', 'engine', 'ready'])
    expect(states(evs)).toMatchObject({ clone: 'done', plugins: 'active', engine: 'pending' })
    const done = [...evs, stageEv({ stages: [
      { id: 'plugins', state: 'done', detail: '1 of 2 installed — frontend-design failed' },
      { id: 'engine', state: 'active' },
    ] })]
    const a = latestStartAttempt(done)!
    expect(a.stages.find(s => s.id === 'plugins')).toMatchObject({
      label: 'Installing plugins', state: 'done', detail: '1 of 2 installed — frontend-design failed',
    })
    expect(a.failed).toBe(false) // a plugin that did not install is not a failed start
  })

  it('a stage the plan lacked still renders, labelled by the pod', () => {
    const evs = [
      stageEv(clusterPlan),
      stageEv({ stages: [{ id: 'plugins', label: 'Installing plugins', state: 'active' }] }),
    ]
    expect(latestStartAttempt(evs)!.stages.find(s => s.id === 'plugins')!.label).toBe('Installing plugins')
  })

  it('a plan without plugins has no plugins stage', () => {
    expect(latestStartAttempt([stageEv(clusterPlan)])!.stages.some(s => s.id === 'plugins')).toBe(false)
  })

  it('a warning stage stays a warning as later stages progress, and is not a failed start', () => {
    const evs = [
      stageEv({
        ...clusterPlan,
        stages: [
          ...clusterPlan.stages!.slice(0, 5),
          { id: 'plugins', label: 'Installing plugins', state: 'warning', detail: "plugins skipped: your plugin list couldn't be loaded" },
          ...clusterPlan.stages!.slice(5),
        ],
      }),
      stageEv({ stages: [{ id: 'clone', state: 'done' }, { id: 'engine', state: 'active' }] }),
    ]
    expect(states(evs)).toMatchObject({ clone: 'done', plugins: 'warning', engine: 'active' })
    expect(latestStartAttempt(evs)!.failed).toBe(false)
    const outcome = withSessionOutcome({ ...session, status: 'error', error_reason: 'boom' }, latestStartAttempt(evs))!
    expect(outcome.stages.find(x => x.id === 'plugins')!.state).toBe('warning')
    expect(outcome.stages.find(x => x.id === 'engine')!.state).toBe('failed')
  })
})
