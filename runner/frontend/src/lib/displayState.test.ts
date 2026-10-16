import { describe, expect, it } from 'vitest'
import { backgroundCount, displayLabel, displayState } from './displayState'

describe('displayState', () => {
  it('is background for an idle session with background tasks running', () => {
    expect(displayState({ status: 'idle', background_tasks: 2 })).toBe('background')
  })

  it('is background for an idle session whose live state has something running', () => {
    expect(displayState({ status: 'idle' }, 1)).toBe('background')
    expect(displayState({ status: 'idle', background_tasks: 0 }, 3)).toBe('background')
  })

  it('takes the live state alone when there is one: nothing running live is idle, whatever the server count says', () => {
    expect(displayState({ status: 'idle', background_tasks: 2 }, 0)).toBe('idle')
    // No live state (null): the server's count decides.
    expect(displayState({ status: 'idle', background_tasks: 2 }, null)).toBe('background')
  })

  it('is idle with nothing running in the background', () => {
    expect(displayState({ status: 'idle' })).toBe('idle')
    expect(displayState({ status: 'idle', background_tasks: 0 }, 0)).toBe('idle')
  })

  it('leaves every other status as it is, background tasks or not', () => {
    for (const status of ['running', 'waiting', 'starting', 'stopped', 'error', 'disconnected', 'ended'] as const) {
      expect(displayState({ status, background_tasks: 2 }, 1)).toBe(status)
    }
  })
})

describe('displayLabel and backgroundCount', () => {
  it('names the background state with its task count', () => {
    expect(displayLabel('background', 2)).toBe('working in background, 2 tasks')
    expect(displayLabel('background', 1)).toBe('working in background, 1 task')
    expect(displayLabel('background')).toBe('working in background')
    expect(displayLabel('idle', 3)).toBe('idle')
    expect(displayLabel('disconnected')).toBe('paused')
  })

  it('counts by the live state when there is one, else by the server', () => {
    expect(backgroundCount({ background_tasks: 2 }, 1)).toBe(1)
    expect(backgroundCount({ background_tasks: 2 }, 0)).toBe(0)
    expect(backgroundCount({ background_tasks: 2 }, null)).toBe(2)
    expect(backgroundCount({}, 3)).toBe(3)
    expect(backgroundCount({})).toBe(0)
  })
})
